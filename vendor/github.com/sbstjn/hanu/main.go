package hanu

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/websocket"
)

type handshakeResponseSelf struct {
	ID string `json:"id"`
}

type handshakeResponse struct {
	Ok    bool                  `json:"ok"`
	Error string                `json:"error"`
	URL   string                `json:"url"`
	Self  handshakeResponseSelf `json:"self"`
}

// Bot is the main object
type Bot struct {
	Socket   *websocket.Conn
	Token    string
	ID       string
	Commands []CommandInterface
}

// New creates a new bot
func New(token string) (*Bot, error) {
	bot := Bot{
		Token: token,
	}

	return bot.Handshake()
}

// Handshake connects to the Slack API to get a socket connection
func (b *Bot) Handshake() (*Bot, error) {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		Dial:                dialer.Dial,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	// Check for HTTP error on connection
	res, err := client.Get(fmt.Sprintf("https://slack.com/api/rtm.start?token=%s", b.Token))
	if err != nil {
		return nil, errors.New("Failed to connect to Slack RTM API")
	}
	defer res.Body.Close()

	// Check for HTTP status code
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("Failed with HTTP Code: %d", res.StatusCode)
	}

	// Read response body
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("Failed to read body from response")
	}

	// Parse response
	var response handshakeResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("Failed to unmarshal JSON: %s", body)
	}

	// Check for Slack error
	if !response.Ok {
		return nil, errors.New(response.Error)
	}

	// Assign Slack user ID
	b.ID = response.Self.ID

	// Connect to websocket
	config, err := websocket.NewConfig(response.URL, "https://api.slack.com/")
	if err != nil {
		return nil, fmt.Errorf("Failed to create config for Websocket: %v", err)
	}
	config.Dialer = dialer
	b.Socket, err = websocket.DialConfig(config)
	if err != nil {
		return nil, fmt.Errorf("Failed to connect to Websocket: %v", err)
	}

	return b, nil
}

// Process incoming message
func (b *Bot) process(message Message) {
	if !message.IsRelevantFor(b.ID) {
		return
	}

	// Strip @BotName from public message
	message.StripMention(b.ID)
	// Strip Slack's link markup
	message.StripLinkMarkup()

	// Check if the message requests the auto-generated help command list
	// or if we need to search for a command matching the request
	if message.IsHelpRequest() {
		b.sendHelp(message)
	} else {
		b.searchCommand(message)
	}
}

// Search for a command matching the message
func (b *Bot) searchCommand(msg Message) {
	var cmd CommandInterface

	for i := 0; i < len(b.Commands); i++ {
		cmd = b.Commands[i]

		match, err := cmd.Get().Match(msg.Text())
		if err == nil {
			cmd.Handle(NewConversation(match, msg, b.Socket))
		}
	}
}

// Send the response for a help request
func (b *Bot) sendHelp(msg Message) {
	var cmd CommandInterface
	help := "Thanks for asking! I can support you with those features:\n\n"

	for i := 0; i < len(b.Commands); i++ {
		cmd = b.Commands[i]

		help = help + "`" + cmd.Get().Text() + "`"
		if cmd.Description() != "" {
			help = help + " *–* " + cmd.Description()
		}

		help = help + "\n"
	}

	if !msg.IsDirectMessage() {
		help = "<@" + msg.User() + ">: " + help
	}

	msg.SetText(help)
	websocket.JSON.Send(b.Socket, msg)
}

// Listen for message on socket
func (b *Bot) Listen() error {
	var msg Message
	pong := make(chan struct{})

	go func() {
		time.Sleep(5 * time.Second)
		count := uint64(100)
		for {
			count++
			if err := websocket.JSON.Send(b.Socket, &Message{ID: count, Type: "ping"}); err != nil {
				log.Printf("Unable to send a ping to the Slack API: %v")
				if err := b.Socket.Close(); err != nil {
					log.Printf("Unable to close websocket: %v")
				}
			}
			select {
			case <-pong:
				time.Sleep(time.Minute)
			case <-time.After(time.Minute):
				log.Printf("Waited more than a minute for a pong, exiting")
				if err := b.Socket.Close(); err != nil {
					log.Printf("Unable to close websocket: %v")
				}
				return
			}
		}
	}()

	for {
		if err := websocket.JSON.Receive(b.Socket, &msg); err != nil {
			return err
		}

		switch msg.Type {
		case "pong":
			select {
			case pong <- struct{}{}:
			default:
			}
		default:
			go b.process(msg)
		}

		// Clean up message after processign it
		msg = Message{}
	}
}

// Command adds a new command with custom handler
func (b *Bot) Command(cmd string, handler Handler) {
	b.Commands = append(b.Commands, NewCommand(cmd, "", handler))
}

// Register registers a Command
func (b *Bot) Register(cmd CommandInterface) {
	b.Commands = append(b.Commands, cmd)
}
