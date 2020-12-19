package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/hossner/bankid"
	"github.com/rs/xid"

	"github.com/gorilla/websocket"
)

// bidConn holds the connection with the server
var bidConn *bankid.Connection

// queueToClient is used to transfer messages from server (through call back function) to web client
var queueToClient = make(chan *wsMsg)

// queueFromClient is used to transfer messages from web client to server
var queueFromClient = make(chan *wsMsg)

// upgrader upgrades the HTTP connection to a websocket connection
var upgrader = websocket.Upgrader{}

// sessMap maps the client session IDs with the channels to correct go routine (that handles the web socket)
var sessMap = make(map[string]chan *wsMsg)

type wsMsg struct {
	Action string `json:"action"`
	Value  string `json:"value"`
	SessID string `json:"id"`
	IPAddr string
}

func main() {
	// Set up simple web server for www directory
	fs := http.FileServer(http.Dir("www"))
	http.Handle("/", http.StripPrefix("/", fs))

	// Set up handler for the websocket request from client
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// Accept upgrade from any origin (don't do this in a production environment!)
		upgrader.CheckOrigin = func(r *http.Request) bool { return true }
		// Upgrade the http request to a websocket
		var conn, err = upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Fatalf("could not upgrade request to websocket: %v", err)
		}
		// Create a session ID for the queues to and from the client
		qid := xid.New().String()
		// Create a channel to write server responses to, to the right session
		sessMap[qid] = make(chan *wsMsg)
		// Start a go routine used to send requests to the web client
		go socketWriter(conn, qid)
		// Start a go routine to listen to incomming requests from the web client
		go socketReader(conn, qid, getRealAddr(r))
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
	go handleClients(bidConn)

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
func socketReader(wsConn *websocket.Conn, id, ip string) {
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
		newMsg.IPAddr = ip
		queueFromClient <- &newMsg
	}
}

// Poll the queueFromClient and send incomming messages to the server
func handleClients(bConn *bankid.Connection) {
	for {
		msg := <-queueFromClient
		switch msg.Action {
		case "pnrAuth":
			// The web client sent a pnr and requests an authentication
			reqs := bankid.Requirements{PersonalNumber: msg.Value}
			bConn.SendRequest(msg.IPAddr, msg.SessID, "", &reqs)
		default:
			log.Println("Unknown command:", "\""+msg.Action+"\"")
		}
	}
}

func getRealAddr(r *http.Request) string {
	if xff := strings.Trim(r.Header.Get("X-Forwarded-For"), ","); len(xff) > 0 {
		addrs := strings.Split(xff, ",")
		lastFwd := addrs[len(addrs)-1]
		if ip := net.ParseIP(lastFwd); ip != nil {
			return ip.String()
		}
	}
	if xri := r.Header.Get("X-Real-Ip"); len(xri) > 0 {
		if ip := net.ParseIP(xri); ip != nil {
			return ip.String()
		}
	}
	if parts := strings.Split(r.RemoteAddr, ":"); len(parts) == 2 {
		return parts[0]
	}
	return ""
}
