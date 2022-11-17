package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Discord color values
const (
	ColorRed       = 0xd00000
	ColorGreen     = 0x36A64F
	ColorGrey      = 0x95A5A6
	AlertNameLabel = "alertname"
)

type AlertManagerData struct {
	Receiver string             `json:"receiver"`
	Status   string             `json:"status"`
	Alerts   AlertManagerAlerts `json:"alerts"`

	GroupLabels       KV `json:"groupLabels"`
	CommonLabels      KV `json:"commonLabels"`
	CommonAnnotations KV `json:"commonAnnotations"`

	ExternalURL string `json:"externalURL"`
	GroupKey    string `json:"groupKey"`
	Version     string `json:"version"`
}

type AlertManagerAlert struct {
	Status       string    `json:"status"`
	Labels       KV        `json:"labels"`
	Annotations  KV        `json:"annotations"`
	StartsAt     time.Time `json:"startsAt"`
	EndsAt       time.Time `json:"endsAt"`
	GeneratorURL string    `json:"generatorURL"`
	Fingerprint  string    `json:"fingerprint"`
}

// KV is a set of key/value string pairs.
type KV map[string]string

// Pair is a key/value string pair.
type Pair struct {
	Name, Value string
}

// Pairs is a list of key/value string pairs.
type Pairs []Pair

// SortedPairs returns a sorted list of key/value pairs.
func (kv KV) SortedPairs() Pairs {
	var (
		pairs     = make([]Pair, 0, len(kv))
		keys      = make([]string, 0, len(kv))
		sortStart = 0
	)
	for k := range kv {
		if k == AlertNameLabel {
			keys = append([]string{k}, keys...)
			sortStart = 1
		} else {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys[sortStart:])

	for _, k := range keys {
		pairs = append(pairs, Pair{k, kv[k]})
	}
	return pairs
}

// Alerts is a list of Alert objects.
type AlertManagerAlerts []AlertManagerAlert

type DiscordMessage struct {
	Content   string        `json:"content"`
	Username  string        `json:"username"`
	AvatarURL string        `json:"avatar_url"`
	Embeds    DiscordEmbeds `json:"embeds"`
}

type DiscordEmbeds []DiscordEmbed

type DiscordEmbed struct {
	Title       string             `json:"title"`
	Description string             `json:"description"`
	URL         string             `json:"url"`
	Color       int                `json:"color"`
	Fields      DiscordEmbedFields `json:"fields"`
}

type DiscordEmbedFields []DiscordEmbedField

type DiscordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

const defaultListenAddress = "127.0.0.1:9094"
const discordEmbedLimit = 10

var (
	webhookURL    = flag.String("webhook.url", os.Getenv("DISCORD_WEBHOOK"), "Discord WebHook URL.")
	listenAddress = flag.String("listen.address", os.Getenv("LISTEN_ADDRESS"), "Address:Port to listen on.")
	username      = flag.String("username", os.Getenv("DISCORD_USERNAME"), "Overrides the predefined username of the webhook.")
	avatarURL     = flag.String("avatar.url", os.Getenv("DISCORD_AVATAR_URL"), "Overrides the predefined avatar of the webhook.")
	verboseMode   = flag.String("verbose", os.Getenv("VERBOSE"), "Verbose mode")
)

func checkWebhookURL(webhookURL string) {
	if webhookURL == "" {
		log.Fatalf("Environment variable 'DISCORD_WEBHOOK' or CLI parameter 'webhook.url' not found.")
	}
	_, err := url.Parse(webhookURL)
	if err != nil {
		log.Fatalf("The Discord WebHook URL doesn't seem to be a valid URL.")
	}

	re := regexp.MustCompile(`https://discord(?:app)?.com/api/webhooks/[0-9]{17,19}/[a-zA-Z0-9_-]+`)
	if ok := re.Match([]byte(webhookURL)); !ok {
		log.Printf("The Discord WebHook URL doesn't seem to be valid.")
	}
}

func sendWebhook(alertManagerData *AlertManagerData) {

	groupedAlerts := make(map[string]AlertManagerAlerts)

	for _, alert := range alertManagerData.Alerts {
		groupedAlerts[alert.Status] = append(groupedAlerts[alert.Status], alert)
	}

	for status, alerts := range groupedAlerts {

		color := findColor(status)

		embeds := DiscordEmbeds{}

		for indx, alert := range alerts {
			embedAlertMessage := DiscordEmbed{
				Title:       getAlertTitle(&alert),
				Color:       color,
				Fields:      DiscordEmbedFields{},
				Description: "\u200B",
			}

			if alert.Annotations["summary"] != "" {
				embedAlertMessage.Fields = append(embedAlertMessage.Fields, DiscordEmbedField{
					Name:  "*Summary:*",
					Value: alert.Annotations["summary"],
				})
			} else if alert.Annotations["message"] != "" {
				embedAlertMessage.Fields = append(embedAlertMessage.Fields, DiscordEmbedField{
					Name:  "*Message:*",
					Value: alert.Annotations["message"],
				})
			} else if alert.Annotations["description"] != "" {
				embedAlertMessage.Fields = append(embedAlertMessage.Fields, DiscordEmbedField{
					Name:  "*Description:*",
					Value: alert.Annotations["description"],
				})
			}
			embedAlertMessage.Fields = append(embedAlertMessage.Fields, DiscordEmbedField{
				Name:  "*Details:*",
				Value: getFormattedLabels(alert.Labels),
			})
			embeds = append(embeds, embedAlertMessage)

			//Check if number of embeds are greater than discord limit and push to discord
			if (indx+1)%(discordEmbedLimit-1) == 0 {
				log.Printf("Sending chunk of data to discord")
				postMessageToDiscord(alertManagerData, status, color, embeds)
				embeds = DiscordEmbeds{}
			}
		}

		if len(embeds) > 0 {
			log.Printf("Sending last chunk of data to discord")
			postMessageToDiscord(alertManagerData, status, color, embeds)
		}
	}
}

func postMessageToDiscord(alertManagerData *AlertManagerData, status string, color int, embeds DiscordEmbeds) {
	discordMessage := buildDiscordMessage(alertManagerData, status, len(embeds), color)
	discordMessage.Embeds = append(discordMessage.Embeds, embeds...)
	discordMessageBytes, _ := json.Marshal(discordMessage)
	if *verboseMode == "ON" || *verboseMode == "true" {
		log.Printf("Sending weebhok message to Discord: %s", string(discordMessageBytes))
	}
	response, err := http.Post(*webhookURL, "application/json", bytes.NewReader(discordMessageBytes))
	if err != nil {
		log.Printf(fmt.Sprint(err))
	}
	// Success is indicated with 2xx status codes:
	statusOK := response.StatusCode >= 200 && response.StatusCode < 300
	if !statusOK {
		responseData, err := ioutil.ReadAll(response.Body)
		if err != nil {
			log.Printf(fmt.Sprint(err))
		}
		log.Printf("Weebhok message to Discord failed: %s", string(responseData))
	}
}

func buildDiscordMessage(alertManagerData *AlertManagerData, status string, numberOfAlerts int, color int) DiscordMessage {
	discordMessage := DiscordMessage{}
	addOverrideFields(&discordMessage)
	messageHeader := DiscordEmbed{
		Title:       fmt.Sprintf("[%s:%d] %s", strings.ToUpper(status), numberOfAlerts, getAlertName(alertManagerData)),
		URL:         alertManagerData.ExternalURL,
		Color:       color,
		Fields:      DiscordEmbedFields{},
		Description: "​",
	}
	discordMessage.Embeds = DiscordEmbeds{messageHeader}
	return discordMessage
}

func addOverrideFields(discordMessage *DiscordMessage) {
	if *username != "" {
		discordMessage.Username = *username
	}
	if *avatarURL != "" {
		discordMessage.AvatarURL = *avatarURL
	}
}

func getFormattedLabels(labels KV) string {
	var builder strings.Builder
	for _, pair := range labels.SortedPairs() {
		builder.WriteString(fmt.Sprintf(" • *%s:* `%s`\n", pair.Name, pair.Value))
	}
	if builder.Len() == 0 {
		builder.WriteString("-")
	}
	return builder.String()
}

func getAlertTitle(alertManagerAlert *AlertManagerAlert) string {
	var builder strings.Builder
	builder.WriteString("*Alert:*")
	builder.WriteString(alertManagerAlert.Annotations["title"])

	if alertManagerAlert.Labels["severity"] != "" {
		builder.WriteString(" - ")
		builder.WriteString(fmt.Sprintf("`%s`", alertManagerAlert.Labels["severity"]))
	}
	return builder.String()
}

func findColor(status string) int {
	color := ColorGrey
	if status == "firing" {
		color = ColorRed
	} else if status == "resolved" {
		color = ColorGreen
	}
	return color
}

func getAlertName(alertManagerData *AlertManagerData) string {
	if alertManagerData.CommonAnnotations["summary"] != "" {
		return alertManagerData.CommonAnnotations["summary"]
	} else if alertManagerData.CommonAnnotations["message"] != "" {
		return alertManagerData.CommonAnnotations["message"]
	} else if alertManagerData.CommonAnnotations["description"] != "" {
		return alertManagerData.CommonAnnotations["description"]
	} else {
		return alertManagerData.CommonLabels["alertname"]
	}
}

func sendRawPromAlertWarn() {
	badString := `This program is suppose to be fed by alert manager.` + "\n" +
		`It is not a replacement for alert manager, it is a ` + "\n" +
		`webhook target for it. Please read the README.md  ` + "\n" +
		`for guidance on how to configure it for alertmanager` + "\n" +
		`or https://prometheus.io/docs/alerting/latest/configuration/#webhook_config`

	log.Print(`/!\ -- You have misconfigured this program -- /!\`)
	log.Print(`--- --                                      -- ---`)
	log.Print(badString)

	discordMessage := DiscordMessage{
		Content: "",
		Embeds: DiscordEmbeds{
			{
				Title:       "misconfigured program",
				Description: badString,
				Color:       ColorGrey,
				Fields:      DiscordEmbedFields{},
			},
		},
	}

	discordMessageBytes, _ := json.Marshal(discordMessage)
	http.Post(*webhookURL, "application/json", bytes.NewReader(discordMessageBytes))
}

func main() {
	flag.Parse()
	checkWebhookURL(*webhookURL)

	if *listenAddress == "" {
		*listenAddress = defaultListenAddress
	}

	log.Printf("Listening on: %s", *listenAddress)
	log.Fatal(http.ListenAndServe(*listenAddress, http.HandlerFunc(handleWebHook)))
}

func handleWebHook(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s - [%s] %s", r.Host, r.Method, r.URL.RawPath)

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		panic(err)
	}

	if *verboseMode == "ON" {
		log.Printf("request payload: %s", string(body))
	}

	alertManagerData := AlertManagerData{}
	err = json.Unmarshal(body, &alertManagerData)
	if err != nil {
		if isRawPromAlert(body) {
			sendRawPromAlertWarn()
			return
		}
		if len(body) > 1024 {
			log.Printf("Failed to unpack inbound alert request - %s...", string(body[:1023]))

		} else {
			log.Printf("Failed to unpack inbound alert request - %s", string(body))
		}
		return
	}
	sendWebhook(&alertManagerData)
}
