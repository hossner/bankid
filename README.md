# bankid
A Golang wrapper around the Swedish BankID appapi v5

bankid.go provides a minimal higher-level wrapper around the Swedish BankID's v.5 appapi.

## Installation
```shell
go get github.com/hossner/bankid
```

## Use case
The purpose of this library is to provide a simple way for a web app to integrate to the Swedish BankID service. The use case for doing so is:
```go
//...
// 1. Define a call back function
function myCallBack(sessionId, message, details string){
    fmt.Println("Session ID:", sessionId, " sent message:", message, " with the details:", details)
}
//...

// 2. Create an instance of the bankid.Connection struct with the call back function as argument.
conn := bankid.New("", myCallBack)
defer bankid.Close

// 3. For a request to the server, call the bankid.Connection.SendRequest method
sessionID := conn.SendRequest("192.168.0.1", "", "", nil)
```

Now, at every status update of the request, the call back function is called, allowing for handling the session accordingly.

If required, more customization is possible by providing a config file (as argument to the bankid.New function) and/or a bankid.Requirement struct to the bankid.Connection.SendRequest method.

More details about the exported structs and functions below.


## Example usage
See example folder


## BankID appapi v5 documentation
See [https://bankid.com/rp/info](https://www.bankid.com/rp/info) for more information about the appapi.

