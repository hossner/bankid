package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"

	"github.com/hossner/bankid"

	"github.com/gorilla/websocket"
)

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

var bidConn *bankid.Connection
var queueToClient = make(chan *wsMsg)
var queueFromClient = make(chan *wsMsg)
var upgrader = websocket.Upgrader{}

var sessMap = make(map[string]chan *wsMsg)

type wsMsg struct {
	Action string `json:"action"`
	Value  string `json:"value"`
	SessID string `json:"id"`
}

func main() {
	// Set up simple web server for www directory
	fs := http.FileServer(http.Dir("www"))
	http.Handle("/", http.StripPrefix("/", fs))

	// Set up handler for the websocket request from client
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// Upgrade the http request to a websocket
		var conn, err = upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Fatalf("could not upgrade request to websocket: %v", err)
		}
		// Create a session ID for the queues to and from the client
		qid := genRand()
		// Create a channel to write server responses to, to the right session
		sessMap[qid] = make(chan *wsMsg)
		// Start a go routine used to send requests to the web client
		go socketWriter(conn, qid)
		// Start a go routine to listen to incomming requests from the web client
		go socketReader(conn, qid)
	})

	// The config file name defaults by the library to 'config.json' in the application working directory
	cfgFileName := ""
	// Create a new connection to the BankID server
	bidConn, err := bankid.New(cfgFileName, callBack)
	if err != nil {
		log.Fatalf("failed to create a connection to the BankID service: %v", err)
	}
	defer bidConn.Close()

	// Start a go routine to handle requests from clients
	go handleClient(bidConn)

	// Start web server, listening to port 8080
	log.Println("Listening to port 8080...")
	http.ListenAndServe(":8080", nil)
}

// Incomming messages from the BankID connection are put on the queue to the client
func callBack(reqID, msg, detail string) {
	/*
		Possible values for 'msg':
			Non error messages:
				'sent': autoStartToken returned as detail
				'cancelled': Caller cancelled the transaction
				'outstandingTransaction':
				'noClient':
				'started':
				'userSign':

			Error messages:
				'error': Internal error from the library, e.g. malformed arguments etc.
				'alreadyInProgress':
				'invalidParameters':
				'unauthorized':
				'notFound':
				'requestTimeout':
				'unsupportedMediaType':
				'internalError':
				'maintenance':
				// If 'failed':
				'expiredTransaction':
				'certificateErr':
				'userCancel': User aborted/cancelled the transaction
				'cancelled': New transaction for the same individual started
				'startFailed':

	*/
	// Create a new instance of wsMsg to hold the values from the server
	newMsg := wsMsg{Action: msg, Value: detail, SessID: reqID}
	// In this example we just push the message to the client through the socketWriter
	sessMap[reqID] <- &newMsg
	// queueToClient <- &newMsg
}

// Poll the queueToClient and send incomming messages to the client
func socketWriter(wsConn *websocket.Conn, id string) {
	more := true
	for more {
		ms, more := <-sessMap[id]
		if more {
			wsConn.WriteJSON(ms)
		}
	}
}

// Listen to requests from the client and put them on the queue from the client
func socketReader(wsConn *websocket.Conn, id string) {
	for {
		_, msg, err := wsConn.ReadMessage()
		if err != nil {
			// This occurs when the client has sent a 'close' message for the websocket
			wsConn.Close()
			close(sessMap[id])
			sessMap[id] = nil
			delete(sessMap, id)
		}
		var newMsg wsMsg
		err = json.Unmarshal(msg, &newMsg)
		if err != nil {
			log.Printf("could not unmarshal request from web client: %v:\n", err)
			return
		}
		newMsg.SessID = id
		queueFromClient <- &newMsg
	}
}

// Poll the queueFromClient and send incomming messages to the server
func handleClient(bConn *bankid.Connection) {
	for {
		msg := <-queueFromClient
		switch msg.Action {
		case "pnrAuth":
			// The web client sent a pnr and requests an authentication
			reqs := bankid.Requirements{PersonalNumber: msg.Value}
			bConn.SendRequest("100.231.180.9", msg.SessID, "", &reqs)
		default:
			log.Println("Unknown command:", "\""+msg.Action+"\"")
		}
	}
}

func genRand() string {
	b := make([]byte, 10)
	for i := range b {
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}
	return string(b)
}
