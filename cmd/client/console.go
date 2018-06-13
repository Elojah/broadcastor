package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/oklog/ulid"

	bc "github.com/elojah/broadcastor"
)

type console struct {
	currentUser bc.ID
	currentRoom bc.ID

	serverAddr string
	client     *http.Client
}

func (c *console) read() {
	for {
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("$>")
		text, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("stdin error:", err)
		}
		c.parse(text)
	}
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
		fmt.Println("you need to log to a room before to send a message")
		return
	}
	msg := bc.Message{
		UserID:  c.currentUser,
		RoomID:  c.currentRoom,
		Content: text,
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		fmt.Println("failed to transform message into packet")
		return
	}
	resp, err := c.client.Post(
		c.serverAddr+"/message/send",
		"application/json; charset=utf-8",
		bytes.NewBuffer(raw),
	)
	if err != nil {
		fmt.Println("failed to send message")
		return
	}
	if resp.StatusCode != 200 {
		fmt.Println("server error")
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
		fmt.Println("unrecognized command")
	}
}

func (c *console) newRoom() {
	resp, err := c.client.Post(
		c.serverAddr+"/room/create",
		"application/json; charset=utf-8",
		nil,
	)
	if err != nil {
		fmt.Println("failed to create room:", err)
		return
	}
	if resp.StatusCode != 200 {
		fmt.Println("server failed to create room with status ", resp.StatusCode)
		return
	}
	decoder := json.NewDecoder(resp.Body)
	var roomID bc.ID
	if err := decoder.Decode(&roomID); err != nil {
		fmt.Println("failed to decode response")
		return
	}
	defer resp.Body.Close()
	fmt.Println(roomID.String())
}

func (c *console) listRooms() {
	resp, err := c.client.Post(
		c.serverAddr+"/room/list",
		"application/json; charset=utf-8",
		nil,
	)
	if err != nil {
		fmt.Println("failed to list rooms:", err)
		return
	}
	if resp.StatusCode != 200 {
		fmt.Println("server failed to list rooms with status ", resp.StatusCode)
		return
	}
	decoder := json.NewDecoder(resp.Body)
	var roomIDs []bc.ID
	if err := decoder.Decode(&roomIDs); err != nil {
		fmt.Println("failed to decode response")
		return
	}
	defer resp.Body.Close()
	for _, roomID := range roomIDs {
		fmt.Println(roomID.String())
	}
}

func (c *console) connect(tokens []string) {
	if len(tokens) < 2 {
		fmt.Println("missing room ID")
		return
	}
	roomID, err := ulid.Parse(tokens[1])
	if err != nil {
		fmt.Println("invalid room ID")
		return
	}
	raw, err := json.Marshal(roomID)
	if err != nil {
		fmt.Println("failed to transform message")
		return
	}
	resp, err := c.client.Post(
		c.serverAddr+"/user/create",
		"application/json; charset=utf-8",
		bytes.NewBuffer(raw),
	)
	if err != nil {
		fmt.Println("failed to listen room", err)
		return
	}
	if resp.StatusCode != 200 {
		fmt.Println("server failed to listen room with status ", resp.StatusCode)
		return
	}
	decoder := json.NewDecoder(resp.Body)
	var userID bc.ID
	if err := decoder.Decode(&userID); err != nil {
		fmt.Println("failed to decode response")
		return
	}
	defer resp.Body.Close()
	fmt.Printf("connected with user ID: %s\n", userID.String())
	c.currentUser = userID
	c.currentRoom = roomID
}
