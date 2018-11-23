package main

// An example implementation of usage of the bankid wrapper library

// Todo:
/*
 - Call SendRequest as a Go routine
*/

import (
	"log"
	"time"

	"github.com/hossner/bankid"
)

// callBack is called at library internal errors, or at every status update in the
// communication with the BankID interface
func callBack(transID, msg, detail string) {
	log.Println(transID + ": \"" + msg + "\",  details: \"" + detail + "\"")
}

func main() {
	// The config file name defaults by the library to 'config.json' in the application working directory
	cfgFileName := ""
	// bankid.New() returns a bankid.Connection struct. Parameter 'callBack' must be != nil. Errors are
	// returned through the callBack function
	bid, err := bankid.New(cfgFileName, callBack)
	if err != nil {
		log.Fatalln(err.Error())
	}
	// The bankid.Requirements struct is only needed if specific requirements are required. Can be set to
	// nil in the call to bankid.Connection.SendRequest() method
	rqm := bankid.Requirements{PersonalNumber: "121212121212", AllowFingerprint: true}
	// The only reqired parameter to bankid.Connection.SendRequest() method is the endUserIP
	// If a transactionID is provided, that will be returned. Otherwise a random string is returned
	// The method is thread safe and can be called as a Go routine
	transID := bid.SendRequest("184.32.45.25", "", "", &rqm)
	time.Sleep(3 * time.Second)
	bid.CancelRequest(transID)
	time.Sleep(2 * time.Second)
	bid.CancelRequest(transID)
	time.Sleep(30 * time.Second)
}
