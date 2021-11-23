package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	jsoniter "github.com/json-iterator/go"
	"github.com/mattn/go-colorable"
	"github.com/nicklaw5/helix"
	"github.com/sirupsen/logrus"
	kvclient "github.com/strimertul/kilovolt-client-go/v6"
)

func check(err error, format string, args ...interface{}) {
	if err != nil {
		args = append(args, err)
		_, _ = fmt.Fprintf(os.Stderr, format+": %s\n", args...)
		os.Exit(1)
	}
}

type FigmentEntry struct {
	Name       string    `json:"name"`
	Count      int       `json:"count"`
	Total      int       `json:"total"`
	LastRedeem time.Time `json:"last_redeem"`
}

type eventSubNotification struct {
	Subscription helix.EventSubSubscription `json:"subscription"`
	Challenge    string                     `json:"challenge"`
	Event        json.RawMessage            `json:"event"`
}

func main() {
	endpoint := flag.String("endpoint", "http://localhost:4337/ws", "Address:port to connect to")
	auth := flag.String("auth", "", "Optional Authorization string")
	prefix := flag.String("prefix", "mirage/", "Prefix/Namespace for keys")
	rewardID := flag.String("reward", "a715bd7d-9454-4ff4-b91f-f74ffc97d63f", "Reward ID to check for")
	password := flag.String("password", "", "Optional password for Kilovolt")
	flag.Parse()

	log := logrus.New()
	_ = prefix

	// Ok this is dumb but listen, I like colors.
	if runtime.GOOS == "windows" {
		log.SetFormatter(&logrus.TextFormatter{ForceColors: true})
		log.SetOutput(colorable.NewColorableStdout())
	}

	headers := http.Header{}
	if *auth != "" {
		headers.Add("Authorization", "Bearer "+*auth)
	}

	client, err := kvclient.NewClient(*endpoint, kvclient.ClientOptions{Headers: headers, Password: *password})
	check(err, "Connection to kilovolt failed")

	log.WithField("endpoint", *endpoint).Info("Connected to Kilovolt")

	// Get chat messages
	chat, err := client.SubscribeKey("twitch/ev/chat-message")
	check(err, "Could not subscribe to chat messages")

	// Get redeems
	webhook, err := client.SubscribeKey("stulbe/ev/webhook")
	check(err, "Could not subscribe to webhooks")

	// Get figment counts
	path := fmt.Sprintf("%sfigments", *prefix)
	var figmentCounts map[string]FigmentEntry
	err = client.GetJSON(path, &figmentCounts)
	if err != nil {
		if err == kvclient.ErrEmptyKey {
			figmentCounts = make(map[string]FigmentEntry)
			log.Info("No figment map found, creating new one")
			_ = client.SetJSON(path, &figmentCounts)
		} else {
			check(err, "Could not get/decode figment map")
		}
	}

	// Subscribe to figment count changes
	figments, err := client.SubscribeKey(path)

	for {
		select {
		case msg := <-figments:
			log.Info("Figment count changed outside miragemod, updating local copy")
			// Update figment count
			err = jsoniter.ConfigFastest.Unmarshal([]byte(msg.Value), &figmentCounts)
			if err != nil {
				log.WithError(err).Error("Failed to decode new figment count, resetting")
				figmentCounts = make(map[string]FigmentEntry)
				continue
			}
		case msg := <-chat:
			// Do nothing
			_ = msg
		case msg := <-webhook:
			var payload eventSubNotification
			err = jsoniter.ConfigFastest.Unmarshal([]byte(msg.Value), &payload)
			if err != nil {
				log.WithError(err).Error("Failed to decode webhook payload")
				continue
			}
			if payload.Subscription.Type == "channel.channel_points_custom_reward_redemption.add" {
				var data helix.EventSubChannelPointsCustomRewardRedemptionEvent
				err = jsoniter.ConfigFastest.Unmarshal(payload.Event, &data)
				if err != nil {
					continue
				}
				if data.Reward.ID == *rewardID {
					total := 0
					count := 0
					if entry, ok := figmentCounts[data.UserID]; ok {
						total = entry.Total
						count = entry.Count
						// Check cooldown
						if time.Since(entry.LastRedeem) < time.Hour*15 {
							_ = say(client, "%s: You can only claim a figment once a day", data.UserName)
							continue
						}
					}
					total += 1
					count += 1
					figmentCounts[data.UserID] = FigmentEntry{
						Name:       data.UserName,
						Count:      count,
						Total:      total,
						LastRedeem: time.Now(),
					}
					err = client.SetJSON(path, &figmentCounts)
					if err != nil {
						log.WithError(err).Error("Failed to update figment map")
					}
					suffix := "th"
					if total < 10 || total > 20 {
						switch total % 10 {
						case 1:
							suffix = "st"
						case 2:
							suffix = "nd"
						case 3:
							suffix = "rd"
						}
					}
					err = say(client, "%s: You claimed your ⭐ %d%s figment! ⭐ (balance: %d)", data.UserName, total, suffix, count)
					log.WithField("user", data.UserID).WithField("reward", data.Reward.Title).Info("Redeemed")
				}
			}
		}
	}
}

func say(client *kvclient.Client, format string, args ...interface{}) error {
	return client.SetKey("twitch/@send-chat-message", fmt.Sprintf(format, args...))
}
