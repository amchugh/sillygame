package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"text/template"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

const bufferSize = 8

// Responsible for data flow from game-logic to connection-controller
type clientStruct struct {
	send    chan []byte
	recieve chan []byte
	close   chan bool
}

var serverAddress = flag.String("addr", "localhost:8080", "The server-side address")
var homeTemplate = template.New("home")

var doAcceptPlayers bool

func home(w http.ResponseWriter, r *http.Request) {
	homeTemplate.Execute(w, "ws://"+r.Host+"/game")
}

var upgrader = websocket.Upgrader{}

var connectionChannel chan clientStruct

var clientchan chan []byte

func handleRead(c *websocket.Conn, cl clientStruct) {
	// Recieve any messages in the channel
	isClosed := false
	for !isClosed {
		select {
		case <-cl.close:
			isClosed = true
			break
		default:
			_, message, err := c.ReadMessage()
			if err != nil {
				fmt.Println("Reading message error:", err, "| Maybe client disconnected?")
				return
			}
			//fmt.Printf("Recieved | message type: %d | message : %s\n", messageType, message)
			cl.recieve <- message
		}
	}
}

func gamesession(w http.ResponseWriter, r *http.Request) {
	// Check to see if we are accepting more players
	/*
		if !doAcceptPlayers {
			fmt.Println("Client attempted connection, but the session is full")
			http.Error(w, "Session is full", 503)
			return
		}
	*/
	// First, we need to upgrade the http session
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println("Upgrade failed")
		// Should send a response to client too
		http.Error(w, "Failed to upgrade session", 500)
	}
	defer c.Close()

	// We have just established a connection with a client.
	// Time to add the client to the list of connections
	sd := make(chan []byte, bufferSize)
	rc := make(chan []byte, bufferSize)
	cl := clientStruct{
		send:    sd,
		recieve: rc,
		close:   make(chan bool, 1),
	}

	// Add this client to the connections
	connectionChannel <- cl

	// Send any messages in the channel
	go func(cl clientStruct) {
		for {
			data := <-cl.send
			err = c.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				fmt.Printf("Failed to send message %s, error: %s\n", data, err)
				break
			}
		}
	}(cl)
	// Do the read as well
	handleRead(c, cl)
	// Need to push a final disconnect message to the logic (just in case)
	cl.recieve <- []byte{leave}
	fmt.Println("client handler: Disconnect!")
}

func refresh(w http.ResponseWriter, r *http.Request) {
	fmt.Println("refreshing!")
	loadPages()
	http.Redirect(w, r, "/", 302)
}

func readFile(n string) string {
	data, err := ioutil.ReadFile(n)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func loadPages() {
	dat := readFile("home.html")
	homeTemplate.Parse(dat)
}

func main() {
	// Setup the server
	http.HandleFunc("/re", refresh)
	http.HandleFunc("/game", gamesession)
	http.HandleFunc("/", home)

	// Read in the page(s)
	loadPages()

	// Create the global channel
	connectionChannel = make(chan clientStruct)

	// Start the server
	fmt.Println("Starting server at address:", *serverAddress)
	go func() {
		log.Fatal(http.ListenAndServe(*serverAddress, nil))
	}()
	fmt.Println("Server started.")
	/*
		for {
			cli := <-connectionChannel
			fmt.Println("Got new client!")
			cli.send <- []byte("Greetings from the server!")
			cli.send <- []byte("I'm sending data. Haha!")
			go func() {
				for {
					foo := <-cli.recieve
					fmt.Println(string(foo))
				}
			}()
		}
	*/

	// Start the game
	playGame(connectionChannel)
}

// --------------------------------------------- //

type player struct {
	client clientStruct
	name   string
	id     byte
}

const (
	accepted   byte = 65
	name       byte = 78
	othername  byte = 79
	message    byte = 80
	leave      byte = 81
	otherleave byte = 82
)

const startPlayers = 2
const maxPlayers = 2

// Handle players joining/leaving the game
// Returns true if a new player joins
func lobby(joinChannel chan clientStruct, players *[]player, maxID int) bool {
	// See if there are players waiting to join
	select {
	case cli := <-joinChannel:
		// A player joined
		// See if we have space to accept them
		if len(*players) < maxPlayers {
			// Every time a player joins,
			// we assign an ID
			// we create a struct
			// we expect to see a name
			// When we get a name, send it to the other players
			// We can accept the player
			// We expect a name message
			data := <-cli.recieve
			// Check to make sure it's the right "message type"
			if data[0] != name {
				// We need to error out
				fmt.Println("Rejected player for bad name connect")
				cli.send <- []byte(string(leave) + "Bad name")
			}
			pname := string(data[1:])
			p := player{
				client: cli,
				name:   pname,
				id:     byte(maxID),
			}
			// Send an accept message to the player
			p.client.send <- []byte{accepted}

			// Send this player to all the connected players!
			bname := stringToBytes(pname)
			msg := append(make([]byte, 2), bname...)
			msg[0] = othername
			msg[1] = p.id
			for _, otherp := range *players {
				otherp.client.send <- msg
			}
			// Now send all the other player's names to this new player!
			for _, otherp := range *players {
				bname = stringToBytes(otherp.name)
				msg = append(make([]byte, 2), bname...)
				msg[0] = othername
				msg[1] = otherp.id
				p.client.send <- msg
			}
			*players = append(*players, p)
			return true
		} else {
			// we cannot accept the player
			// Send a disconnect message to them
			disconnect(cli, "Session is full")
		}
	default:
	}
	return false
}

func disconnect(cli clientStruct, message string) {
	select {
	case cli.send <- []byte(string(leave) + message):
	default:
		fmt.Println("game: Failed to send disconnect message")
	}
	cli.close <- true
}

func playGame(joinChannel chan clientStruct) {
	// The first thing we need to do is wait for enough players to join.
	players := make([]player, 0)

	// Set up the ID counter
	var id int = 0

	// Accept players until the game is full
	for len(players) < startPlayers {
		if lobby(joinChannel, &players, id) {
			id++
		}
	}

	// Now we can start the loop
	for {
		// Let's try and recieve a message from every client now
		breakup := false
		for currentIndex, p := range players {
			consumeMore := true
			for consumeMore {
				select {
				case msg := <-p.client.recieve:
					// We recieved a message
					//fmt.Println(p.name, ":", string(msg)[1:])
					// See if the client message is a "message" type
					if msg[0] == message {
						// User sent a message. We need to send this message to all other clients
						msgdata := stringToBytes(string(msg[1:]))
						rawmsg := append(make([]byte, 2), msgdata...)
						rawmsg[0] = message
						rawmsg[1] = p.id
						for pIndex, otherP := range players {
							if currentIndex != pIndex {
								otherP.client.send <- rawmsg
							}
						}
					}
					// We need to handle a disconnect message (whether it be internal or from the client)
					if msg[0] == leave {
						// This user has disconnected (for whatever reason)
						// Send a message to all the other players about what has happened
						msg := []byte{otherleave, p.id}
						for pIndex, otherP := range players {
							if currentIndex != pIndex {
								otherP.client.send <- msg
							}
						}
						// Remove them from players
						// We move the last element to this position, then resize the array
						players[currentIndex] = players[len(players)-1]
						players = players[:len(players)-1]
						// Now, I think we need to break to ensure we don't break any indexing rules
						breakup = true
					}
				default:
					consumeMore = false
				}
				if breakup {
					break
				}
			}
			if breakup {
				break
			}
		}
		// Let's also continue to manage the lobby
		if lobby(joinChannel, &players, id) {
			id++
		}
	}

}

func stringToBytes(s string) []byte {
	bts := make([]byte, len(s)*utf8.UTFMax)
	c := 0
	for _, r := range s {
		c += utf8.EncodeRune(bts[c:], r)
	}
	return bts[:c]
}
