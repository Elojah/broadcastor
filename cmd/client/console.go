package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	ui "github.com/gizak/termui"
	"github.com/oklog/ulid"

	bc "github.com/elojah/broadcastor"
)

const (
	maxMessages = 7
)

type console struct {
	currentUser bc.ID
	currentRoom bc.ID

	messages [maxMessages]string

	serverAddr string
	client     *http.Client

	msgbox   *ui.Par
	writebox *ui.Par
}

func (c *console) addMessage(msg string) {
	if c.messages[maxMessages-1] != "" {
		for i := 0; i < maxMessages-1; i++ {
			c.messages[i] = c.messages[i+1]
		}
		c.messages[maxMessages-1] = msg
	} else {
		for i := range c.messages {
			if c.messages[i] == "" {
				c.messages[i] = msg
				break
			}
		}
	}
	c.msgbox.Text = strings.Join(c.messages[:], "\n")
	ui.Render(c.msgbox)
}

func (c *console) start() {
	err := ui.Init()
	if err != nil {
		c.addMessage("failed to init ui")
		return
	}
	defer ui.Close()

	c.msgbox = ui.NewPar("")
	c.msgbox.Height = maxMessages + 2
	c.msgbox.Width = 150
	c.msgbox.TextFgColor = ui.ColorWhite
	c.msgbox.BorderLabel = "‛¯¯٭٭¯¯(▫▫)¯¯٭٭¯¯’"
	c.msgbox.BorderFg = ui.ColorCyan

	c.writebox = ui.NewPar("")
	c.writebox.Y = maxMessages + 2
	c.writebox.Height = 3
	c.writebox.Width = 150
	c.writebox.TextFgColor = ui.ColorWhite
	c.writebox.BorderLabel = ""
	c.writebox.BorderFg = ui.ColorMagenta

	ui.Handle("/sys/kbd/C-c", func(ui.Event) {
		ui.StopLoop()
	})

	ui.Handle("/sys/kbd", func(event ui.Event) {
		s := event.Data.(ui.EvtKbd)
		c.writebox.Text += s.KeyStr
		ui.Render(c.writebox)
	})

	ui.Handle("/sys/kbd/<enter>", func(event ui.Event) {
		c.parse(c.writebox.Text)
		c.writebox.Text = ""
		ui.Render(c.writebox)
	})

	ui.Handle("/sys/kbd/<space>", func(_ ui.Event) {
		c.writebox.Text += " "
		ui.Render(c.writebox)
	})

	ui.Handle("/sys/kbd/C-8", func(_ ui.Event) {
		lenTxt := len(c.writebox.Text)
		if lenTxt == 0 {
			return
		}
		c.writebox.Text = c.writebox.Text[:lenTxt-1]
		ui.Render(c.writebox)
	})

	ui.Render(c.msgbox, c.writebox)
	ui.Loop()
}

func (c *console) parse(text string) {
	if len(text) == 0 {
		return
	}
	if text[0] == '/' {
		c.command(text[1:])
		return
	}
	if c.currentUser.Time() == 0 || c.currentRoom.Time() == 0 {
		c.addMessage("you need to log to a room before to send a message")
		return
	}
	msg := bc.Message{
		UserID:  c.currentUser,
		RoomID:  c.currentRoom,
		Content: text,
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		c.addMessage("failed to transform message into packet")
		return
	}
	resp, err := c.client.Post(
		c.serverAddr+"/message/send",
		"application/json; charset=utf-8",
		bytes.NewBuffer(raw),
	)
	if err != nil {
		c.addMessage("failed to send message")
		return
	}
	if resp.StatusCode != 200 {
		c.addMessage("server error")
		return
	}
}

func (c *console) command(text string) {
	tokens := strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n'
	})
	if len(tokens) == 0 {
		return
	}
	switch tokens[0] {
	case "newroom":
		c.newRoom()
	case "rooms":
		c.listRooms()
	case "connect":
		c.connect(tokens)
	default:
		c.addMessage("unrecognized command")
	}
}

func (c *console) newRoom() {
	resp, err := c.client.Post(
		c.serverAddr+"/room/create",
		"application/json; charset=utf-8",
		nil,
	)
	if err != nil {
		c.addMessage("failed to create room:" + err.Error())
		return
	}
	if resp.StatusCode != 200 {
		c.addMessage("server failed to create room with status " + resp.Status)
		return
	}
	decoder := json.NewDecoder(resp.Body)
	var roomID bc.ID
	if err := decoder.Decode(&roomID); err != nil {
		c.addMessage("failed to decode response")
		return
	}
	defer resp.Body.Close()
	c.addMessage(roomID.String())
	c.currentRoom = roomID
}

func (c *console) listRooms() {
	resp, err := c.client.Post(
		c.serverAddr+"/room/list",
		"application/json; charset=utf-8",
		nil,
	)
	if err != nil {
		c.addMessage("failed to list rooms:" + err.Error())
		return
	}
	if resp.StatusCode != 200 {
		c.addMessage("server failed to list rooms with status " + resp.Status)
		return
	}
	decoder := json.NewDecoder(resp.Body)
	var roomIDs []bc.ID
	if err := decoder.Decode(&roomIDs); err != nil {
		c.addMessage("failed to decode response")
		return
	}
	defer resp.Body.Close()
	for _, roomID := range roomIDs {
		c.addMessage(roomID.String())
	}
}

func (c *console) connect(tokens []string) {
	var roomID bc.ID
	if len(tokens) < 2 {
		if c.currentRoom.Time() == 0 {
			c.addMessage("missing room ID")
			return
		}
		roomID = c.currentRoom
	} else {
		var err error
		roomID, err = ulid.Parse(tokens[1])
		if err != nil {
			c.addMessage("invalid room ID")
			return
		}
	}
	raw, err := json.Marshal(roomID)
	if err != nil {
		c.addMessage("failed to transform message")
		return
	}
	resp, err := c.client.Post(
		c.serverAddr+"/user/create",
		"application/json; charset=utf-8",
		bytes.NewBuffer(raw),
	)
	if err != nil {
		c.addMessage("failed to listen room:" + err.Error())
		return
	}
	if resp.StatusCode != 200 {
		c.addMessage("server failed to listen room with status " + resp.Status)
		return
	}
	decoder := json.NewDecoder(resp.Body)
	var userID bc.ID
	if err := decoder.Decode(&userID); err != nil {
		c.addMessage("failed to decode response")
		return
	}
	defer resp.Body.Close()
	c.addMessage(fmt.Sprintf("connected with user ID: %s\n", userID.String()))
	c.currentUser = userID
	c.currentRoom = roomID
}
