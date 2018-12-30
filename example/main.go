package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/hossner/bankid"

	"github.com/gorilla/websocket"
)

var bidConn *bankid.Connection
var queueToClient = make(chan *wsMsg)
var queueFromClient = make(chan *wsMsg)
var upgrader = websocket.Upgrader{}

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
		var conn, _ = upgrader.Upgrade(w, r, nil)
		// Start a go routine used to send requests to the web client
		go socketWriter(conn)
		// Start a go routine to listen to incomming requests from the web client
		go socketReader(conn)
	})

	// The config file name defaults by the library to 'config.json' in the application working directory
	cfgFileName := ""
	// Create a new connection to the BankID server
	bidConn, err := bankid.New(cfgFileName, callBack)
	if err != nil {
		log.Fatalln(err.Error())
	}
	defer bidConn.Close()

	// Start a go routine to
	go handleClient(bidConn)

	// Start web server, listening to port 8080
	fmt.Println("Listening to port 8080...")
	http.ListenAndServe(":8080", nil)
}

// Incomming messages from the BankID connection are put on the queue to the client
func callBack(sessId, msg, detail string) {
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
	newMsg := wsMsg{Action: msg, Value: detail, SessID: sessId}
	fmt.Println("callBack:", newMsg)
	// In this example we just push the message to the client
	queueToClient <- &newMsg
}

func socketWriter(wsConn *websocket.Conn) {
	// fmt.Println("socketWriter started...")
	for {
		msg := <-queueToClient
		// fmt.Println("socketWriter:", msg)
		wsConn.WriteJSON(msg)
	}
}

// Listen to requests from the client and put them on the queue from the client
func socketReader(wsConn *websocket.Conn) {
	// fmt.Println("socketReader started...")
	for {
		_, msg, err := wsConn.ReadMessage()
		// fmt.Println("socketReader receiving...")
		if err != nil {
			// This occurs when the client has sent a 'close' message
			wsConn.Close()
		}
		var newMsg wsMsg
		json.Unmarshal(msg, &newMsg)
		// fmt.Println("socketReader:", newMsg)
		queueFromClient <- &newMsg
	}
}

// Poll the queueToClient and send incomming messages to the client
// Listen to the queueFromClient
func handleClient(bConn *bankid.Connection) {
	for {
		msg := <-queueFromClient
		switch msg.Action {
		case "pnrAuth":
			// The web client sent a pnr and requests an authentication
			reqs := bankid.Requirements{PersonalNumber: msg.Value}
			bConn.SendRequest("100.231.180.9", "", "", &reqs)
		default:
			fmt.Println("Unknown command:", "\""+msg.Action+"\"")
		}
	}
}
