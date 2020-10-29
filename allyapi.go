// https://www.ally.com/api/invest/documentation/getting-started/
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dghubble/oauth1"
	"github.com/keybase/go-keychain"
)

var version = "undefined"
var showVersionFlag, streamFlag *bool
var symbols *string
var client = newAllyClient()
var wg sync.WaitGroup

type allyClient struct {
	*http.Client
	APICallsRemaining int
	mu                sync.Mutex
}

type quoteArray []map[string]string

type apiResponse struct {
	Status   string `json:",omitempty"`
	Response *struct {
		ID          string `json:"@id,omitempty"`
		ElapsedTime int    `json:",string,omitempty"`
		Error       string `json:",omitempty"`
		Quotes      *struct {
			QuoteType string     `json:",omitempty"`
			Quote     quoteArray `json:",omitempty"`
		} `json:",omitempty"`
	} `json:",omitempty"`
	Trade *struct {
		Cvol      int                    `json:",string,omitempty"`
		DateTime  string                 `json:",omitempty"`
		Exch      map[string]interface{} `json:",omitempty"`
		Last      float32                `json:",string,omitempty"`
		Symbol    string                 `json:",omitempty"`
		Timestamp int64                  `json:",string,omitempty"`
		Vl        int                    `json:",string,omitempty"`
		Vwap      float32                `json:",string,omitempty"`
	} `json:",omitempty"`
}

func (qa *quoteArray) UnmarshalJSON(data []byte) error {
	if len(data) < 1 {
		return errors.New("No input")
	}
	switch data[0] {
	case '[':
		var q []map[string]string
		if err := json.Unmarshal(data, &q); err != nil {
			return err
		}
		*qa = q
	case '{':
		var mp map[string]string
		if err := json.Unmarshal(data, &mp); err != nil {
			return err
		}
		*qa = quoteArray{mp}
	}
	return nil
}

func timestampToDate(str string) time.Time {
	timestampArr := make([]int64, 2)
	var err error
	for i, s := range strings.Split(str, ".") {
		if s != "" {
			timestampArr[i], err = strconv.ParseInt(s, 10, 64)
			if err != nil {
				log.Println("Error converting timestamp string to int64")
			}
		}
	}
	return time.Unix(timestampArr[0], timestampArr[1])
}

func (ac *allyClient) doAPICall(endpoint string, method string, data map[string][]string) (string, error) {

	if strings.HasPrefix(endpoint, "/") {
		endpoint = "https://devapi.invest.ally.com/v1" + endpoint
	}

	var dataString string
	if data != nil {
		urlValues := url.Values{}

		for k, vs := range data {
			for _, v := range vs {
				urlValues.Add(k, v)
			}
		}
		dataString = urlValues.Encode()
	} else {
		dataString = ""
	}

	req, err := http.NewRequest(method, endpoint, strings.NewReader(dataString))
	if err != nil {
		return "", err
	}

	if req.Method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := ac.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	for decoder.More() {

		var m apiResponse
		err := decoder.Decode(&m)
		if err != nil {
			return "", err
		}

		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return "", err
		}
		fmt.Println(string(b))
	}

	// Interesting response headers:
	// X-Ratelimit-Used: Number of requests sent against the current limit
	// X-Ratelimit-Expire: When the current limit will expire (Unix timestamp)
	// X-Ratelimit-Limit: Total number of requests allowed in the call limit
	// X-Ratelimit-Remaining: Number of requests allowed against the current lim it
	wg.Add(1)
	go func() {
		defer wg.Done()
		ac.mu.Lock()
		defer ac.mu.Unlock()

		ac.APICallsRemaining, err = strconv.Atoi(resp.Header["X-Ratelimit-Remaining"][0])
		if err != nil {
			log.Println("Unable to determine API calls remaining")
		}

		if ac.APICallsRemaining < 10 {
			fmt.Printf("Warning: only %v API calls remaining\n", ac.APICallsRemaining)
			expiration := timestampToDate(resp.Header["X-Ratelimit-Expire"][0])
			fmt.Printf("Current limit set to expire at %v\n", expiration)
		}
	}()

	return "", nil
}

func (ac *allyClient) get(url string) (string, error) {
	return ac.doAPICall(url, "GET", nil)
}

func (ac *allyClient) post(url string, data map[string][]string) (string, error) {
	return ac.doAPICall(url, "POST", data)
}

func (ac *allyClient) streamQuotes(symbols []string) (string, error) {
	quotesEndpoint := "https://devapi-stream.invest.ally.com/v1/market/quotes.json"

	data := make(map[string][]string, 1)
	data["symbols"] = []string{strings.Join(symbols, ",")}

	body, err := ac.post(quotesEndpoint, data)

	if err != nil {
		return "", err
	}
	return body, nil
}

func (ac *allyClient) getQuotes(symbols []string) (string, error) {
	quotesEndpoint := "/market/ext/quotes.json"

	data := make(map[string][]string, 1)
	data["symbols"] = []string{strings.Join(symbols, ",")}

	body, err := ac.post(quotesEndpoint, data)
	if err != nil {
		return "", err
	}
	return body, nil
}

func newAllyClient() *allyClient {
	consumerKey, err := getCredsFromKeychain("TradeKing", "consumer_key")
	if err != nil {
		log.Fatalf("Error setting up TradeKing client: %v\n", err)
	}

	consumerSecret, err := getCredsFromKeychain("TradeKing", "consumer_secret")
	if err != nil {
		log.Fatalf("Error setting up TradeKing client: %v\n", err)
	}

	accessToken, err := getCredsFromKeychain("TradeKing", "access_token")
	if err != nil {
		log.Fatalf("Error setting up TradeKing client: %v\n", err)
	}

	accessSecret, err := getCredsFromKeychain("TradeKing", "access_secret")
	if err != nil {
		log.Fatalf("Error setting up TradeKing client: %v\n", err)
	}

	config := oauth1.NewConfig(consumerKey, consumerSecret)
	token := oauth1.NewToken(accessToken, accessSecret)

	client := allyClient{
		config.Client(oauth1.NoContext, token),
		0,
		sync.Mutex{},
	}

	return &client
}

func printVersion() {
	fmt.Println("allyapi version:", version)
	os.Exit(0)
}

// Try to get credentials from keychain
func getCredsFromKeychain(service, account string) (string, error) {
	query := keychain.NewItem()
	query.SetSecClass(keychain.SecClassGenericPassword)
	query.SetService(service)
	query.SetAccount(account)
	query.SetMatchLimit(keychain.MatchLimitOne)
	query.SetReturnData(true)
	results, err := keychain.QueryItem(query)
	if err != nil {
		return "", err
	} else if len(results) != 1 {
		return "", fmt.Errorf("got %v results", len(results))
	}
	password := string(results[0].Data)
	return password, nil
}

func showAccounts() (string, error) {
	accountsURL := "/accounts.json"

	accounts, err := client.get(accountsURL)
	if err != nil {
		return "", err
	}
	return accounts, nil
}

func init() {
	showVersionFlag = flag.Bool("version", false, "Print version")
	streamFlag = flag.Bool("stream", false, "Stream symbols")
	symbols = flag.String("symbols", "", "Comma-separated list of symbols to search for quotes")
}

func main() {

	flag.Parse()
	if *showVersionFlag {
		printVersion()
	}

	switch *symbols {
	case "":
		flag.PrintDefaults()
	default:
		symbolsSlice := strings.Split(*symbols, ",")

		if *streamFlag {
			quotes, err := client.streamQuotes(symbolsSlice)
			if err != nil {
				log.Fatalf("error streaming quotes: %v", err)
			}
			fmt.Println(quotes)
		} else {
			quotes, err := client.getQuotes(symbolsSlice)
			if err != nil {
				log.Fatalf("error getting quotes: %v", err)
			}
			fmt.Println(quotes)
		}
	}
	wg.Wait()
}
