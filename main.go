package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/mr-tron/base58"
	"golang.org/x/net/proxy"
)

const (
	accountsPath = "./accounts.txt"
	proxiesPath  = "./proxies.txt"
)

var (
	green   = color.New(color.FgGreen).SprintFunc()
	yellow  = color.New(color.FgYellow).SprintFunc()
	red     = color.New(color.FgRed).SprintFunc()
	cyan    = color.New(color.FgCyan).SprintFunc()
	magenta = color.New(color.FgMagenta).SprintFunc()
	gray    = color.New(color.FgHiBlack).SprintFunc()
)

type Account struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
	PrivateKey   string `json:"privateKey"`
}

func logMessage(pubKey, message, logType string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	var messageColor, pubKeyColor func(a ...interface{}) string

	switch logType {
	case "success":
		messageColor = green
		pubKeyColor = yellow
	case "error":
		messageColor = red
		pubKeyColor = yellow
	case "warning":
		messageColor = yellow
		pubKeyColor = yellow
	case "system":
		messageColor = cyan
		pubKeyColor = yellow
	default:
		messageColor = magenta
		pubKeyColor = yellow
	}

	if logType == "system" {
		fmt.Printf("[%s] %s\n", gray(timestamp), messageColor(message))
	} else {
		if strings.HasPrefix(message, "Processing") && pubKey != "" && pubKey != "UNKNOWN" {
			fmt.Printf("[%s] %s %s\n", gray(timestamp), messageColor("Processing"), pubKeyColor(pubKey))
		} else {
			fmt.Printf("[%s] %s\n", gray(timestamp), messageColor(message))
		}
	}
}

func readAccounts() []Account {
	data, err := ioutil.ReadFile(accountsPath)
	if err != nil {
		logMessage("SYSTEM", fmt.Sprintf("Error reading accounts: %s", err.Error()), "error")
		return []Account{}
	}

	lines := strings.Split(string(data), "\n")
	accounts := make([]Account, 0)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Split(line, ":")
		if len(parts) != 3 {
			continue
		}

		accounts = append(accounts, Account{
			Token:        parts[0],
			RefreshToken: parts[1],
			PrivateKey:   parts[2],
		})
	}

	return accounts
}

func readProxies() []string {
	data, err := ioutil.ReadFile(proxiesPath)
	if err != nil {
		return []string{}
	}

	lines := strings.Split(string(data), "\n")
	proxies := make([]string, 0)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			proxies = append(proxies, line)
		}
	}

	return proxies
}

func updateAccountFile(accounts []Account) {
	var content strings.Builder
	for _, acc := range accounts {
		content.WriteString(fmt.Sprintf("%s:%s:%s\n", acc.Token, acc.RefreshToken, acc.PrivateKey))
	}

	err := ioutil.WriteFile(accountsPath, []byte(content.String()), 0644)
	if err != nil {
		logMessage("SYSTEM", fmt.Sprintf("Error updating accounts file: %s", err.Error()), "error")
	}
}

func getPublicKey(privateKey string) string {
	if privateKey == "" {
		return "UNKNOWN"
	}

	decoded, err := base58.Decode(privateKey)
	if err != nil {
		logMessage("SYSTEM", fmt.Sprintf("Error decoding private key: %s", err.Error()), "error")
		return "UNKNOWN"
	}

	// Ensure the decoded private key is 64 bytes long
	if len(decoded) != 64 {
		logMessage("SYSTEM", "Invalid private key length", "error")
		return "UNKNOWN"
	}

	// Extract the first 32 bytes (seed) from the private key
	seed := decoded[:32]

	// Generate the keypair from the seed
	key := ed25519.NewKeyFromSeed(seed)
	return base58.Encode(key.Public().(ed25519.PublicKey))
}

func customFetch(proxyURL string) (*http.Client, error) {
	if proxyURL == "" {
		return http.DefaultClient, nil
	}

	proxyURLParsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}

	dialer, err := proxy.FromURL(proxyURLParsed, proxy.Direct)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		Dial: dialer.Dial,
	}

	return &http.Client{Transport: transport}, nil
}

func getLoginMessage(client *http.Client) (string, error) {
	resp, err := client.Get("https://api.assisterr.ai/incentive/auth/login/get_message/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

func signLoginMessage(message, privateKey string) (string, string, error) {
	decodedKey, err := base58.Decode(privateKey)
	if err != nil {
		return "", "", err
	}

	// Ensure the decoded private key is 64 bytes long
	if len(decodedKey) != 64 {
		return "", "", fmt.Errorf("invalid private key length")
	}

	// Extract the first 32 bytes (seed) from the private key
	seed := decodedKey[:32]

	// Generate the keypair from the seed
	key := ed25519.NewKeyFromSeed(seed)

	// Sign the message
	signature := ed25519.Sign(key, []byte(message))

	return base58.Encode(signature), base58.Encode(key.Public().(ed25519.PublicKey)), nil
}

func handleLogin(client *http.Client, message, privateKey string) (map[string]interface{}, error) {
	signature, publicKey, err := signLoginMessage(message, privateKey)
	if err != nil {
		return nil, err
	}

	payload := map[string]string{
		"message":   message,
		"signature": signature,
		"key":       publicKey,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := client.Post("https://api.assisterr.ai/incentive/auth/login/", "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	return result, err
}

func handleTokenRefresh(client *http.Client, refreshToken string) (map[string]interface{}, error) {
	req, err := http.NewRequest("POST", "https://api.assisterr.ai/incentive/auth/refresh_token/", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", refreshToken))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	return result, err
}

func claimDaily(client *http.Client, token string) (map[string]interface{}, error) {
	req, err := http.NewRequest("POST", "https://api.assisterr.ai/incentive/users/me/daily_points/", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	return result, err
}

func checkUserStatus(client *http.Client, token string) (map[string]interface{}, error) {
	req, err := http.NewRequest("GET", "https://api.assisterr.ai/incentive/users/me/", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	return result, err
}

func getUserMeta(client *http.Client, token string) (map[string]interface{}, error) {
	req, err := http.NewRequest("GET", "https://api.assisterr.ai/incentive/users/me/meta/", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	return result, err
}

func processAccount(account Account, proxyURL string) Account {
	client, err := customFetch(proxyURL)
	if err != nil {
		logMessage("", fmt.Sprintf("Error creating HTTP client: %s", err.Error()), "error")
		return account
	}

	publicKey := getPublicKey(account.PrivateKey)
	logMessage(publicKey, "Processing", "info")

	currentAccount := account
	userStatus, err := checkUserStatus(client, currentAccount.Token)
	if err != nil {
		logMessage("", fmt.Sprintf("Error checking user status: %s", err.Error()), "error")
		return account
	}

	if userStatus["id"] == nil {
		logMessage("", "Token expired, attempting refresh...", "info")
		refreshResult, err := handleTokenRefresh(client, currentAccount.RefreshToken)
		if err != nil {
			logMessage("", fmt.Sprintf("Error refreshing token: %s", err.Error()), "error")
			return account
		}

		if refreshResult["access_token"] != nil {
			currentAccount.Token = refreshResult["access_token"].(string)
			currentAccount.RefreshToken = refreshResult["refresh_token"].(string)
			logMessage("", "Token refreshed successfully", "success")
			userStatus, err = checkUserStatus(client, currentAccount.Token)
			if err != nil {
				logMessage("", fmt.Sprintf("Error checking user status after refresh: %s", err.Error()), "error")
				return account
			}
		} else {
			logMessage("", "Token refresh failed, attempting new login...", "warning")
			message, err := getLoginMessage(client)
			if err != nil {
				logMessage("", fmt.Sprintf("Error getting login message: %s", err.Error()), "error")
				return account
			}

			loginResult, err := handleLogin(client, strings.Trim(message, `'"`), currentAccount.PrivateKey)
			if err != nil {
				logMessage("", fmt.Sprintf("Error logging in: %s", err.Error()), "error")
				return account
			}

			if loginResult["access_token"] == nil {
				logMessage("", "Login failed", "error")
				return account
			}

			currentAccount.Token = loginResult["access_token"].(string)
			currentAccount.RefreshToken = loginResult["refresh_token"].(string)
			logMessage("", "New login successful", "success")
		}
	}

	meta, err := getUserMeta(client, currentAccount.Token)
	if err != nil {
		logMessage("", fmt.Sprintf("Error getting user meta: %s", err.Error()), "error")
		return account
	}

	if meta["daily_points_start_at"] != nil {
		nextClaim, err := time.Parse(time.RFC3339, meta["daily_points_start_at"].(string))
		if err != nil {
			logMessage("", fmt.Sprintf("Error parsing next claim time: %s", err.Error()), "error")
			return account
		}

		if nextClaim.After(time.Now()) {
			timeUntil := nextClaim.Sub(time.Now()).Minutes()
			logMessage("", fmt.Sprintf("Next claim available in %.0f minutes", timeUntil), "info")
			return currentAccount
		}
	}

	claimResult, err := claimDaily(client, currentAccount.Token)
	if err != nil {
		logMessage("", fmt.Sprintf("Error claiming daily points: %s", err.Error()), "error")
		return account
	}

	if claimResult["points"] != nil {
		logMessage("", fmt.Sprintf("Claim successful! Received %v points", claimResult["points"]), "success")
		nextClaimTime, err := time.Parse(time.RFC3339, claimResult["daily_points_start_at"].(string))
		if err != nil {
			logMessage("", fmt.Sprintf("Error parsing next claim time: %s", err.Error()), "error")
			return account
		}

		logMessage("", fmt.Sprintf("Next claim available at %s", nextClaimTime.Local().Format("2006-01-02 15:04:05")), "info")
	} else {
		logMessage("", fmt.Sprintf("Claim failed: %v", claimResult), "error")
	}

	return currentAccount
}

func main() {
	fmt.Println(cyan("Autoclaim Daily Started!\n"))

	accounts := readAccounts()
	proxies := readProxies()

	if len(proxies) > 0 {
		fmt.Println(yellow(fmt.Sprintf("Loaded %d proxies", len(proxies))))
	} else {
		fmt.Println(red("No proxies found, using direct connection"))
	}

	fmt.Println(magenta(fmt.Sprintf("Processing %d accounts\n", len(accounts))))

	updatedAccounts := make([]Account, 0)

	for i, account := range accounts {
		proxyURL := ""
		if len(proxies) > 0 {
			proxyURL = proxies[i%len(proxies)]
		}

		updatedAccount := processAccount(account, proxyURL)
		updatedAccounts = append(updatedAccounts, updatedAccount)
	}

	updateAccountFile(updatedAccounts)
	fmt.Println()
	logMessage("SYSTEM", "All accounts processed, waiting for next cycle...", "success")

	time.Sleep(3600 * time.Second)
	main()
}

func init() {
	fmt.Println(cyan(`
╔═══════════════════════════════════════════╗
║         Assisterr Daily Claimer           ║
║       https://github.com/im-hanzou        ║
╚═══════════════════════════════════════════╝
`))
}
