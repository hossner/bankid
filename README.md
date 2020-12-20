# bankid
A Golang wrapper around the Swedish BankID appapi v5.1

bankid.go provides a minimal higher-level wrapper around the Swedish BankID's v.5.1 appapi.

## Note!
This library is still under heavy construction, and you are **not!** encouraged to use it in a production environment!

## Installation
```shell
go get github.com/hossner/bankid
```

## Use case
The purpose of this library is to provide a simple way for Go applications to integrate with the [Swedish BankID service](https://www.bankid.com). The use case for doing so is:
1. Define a call back function

```go
function myCallBack(sessionId, message, details string){
    fmt.Println("Session ID:", sessionId, " sent message:", message, " with the details:", details)
}
```
2. Create an instance of the bankid.Connection struct with the call back function as argument.
```go
conn := bankid.New("", myCallBack)
defer bankid.Close
```
3. For each request from the client, call the bankid.Connection.SendRequest method. Note that the remote client's IP address must be provided as the first argument.
```go
sessionID := conn.SendRequest("192.168.0.1", "", "", nil, nil)
```

Now, at every status update of the request the call back function ```myCallBack``` is called, allowing for handling the session accordingly.

If required, more customization is possible through the configuration file (path provided as argument to the ```bankid.New``` function) and/or a ```bankid.Requirement``` struct, provided as argument to the bankid.Connection.SendRequest method at each request.

More details about the exported structs and functions below.


## The config file
The configuration file is a JSON formatted text file, the different settings explained below.

### Section ```certStore```
User authenticated TLS is used to establish an authenticated connection with the BankID service. The required client certificate with key, and the CA certificate, are stored in the ```certStorePath``` directory. The client certificate and key are stored in ```userP12FileName```, with the password for the file in ```userPrivateKeyPassword```. The CA certificate is stored in ```caCertFileName```.

### Section ```httpClientConfig```
The ```Host``` and ```Content-type``` values are used in the HTTP client when comunicating with the BankID service. The values provided in the example configuration file are currently the only ones accepted.

### ```serviceURL```
The ```serviceURL``` can be set to point to either the test endpoint or the production end point. The value in the provided example configuration file points to the test endpoint.

### ```pollDelay```
The ```pollDelay``` value (in milliseconds) defines how often the BankID service should be polled for status updates for the ongoing requests. Values lower than 2000 (2 seconds) are not allowed and will default to 2000.

### ```logFile```
Path to log file to be used by the library. If this value is set to empty string, logging is done to stderr.

### ```logLevel```
Integer value 0-5 to enable/disable logging. A value of 0 disables logging, 1 equals debug logging, 2 warnings, 3 errors, 4 and 5 critical log messages. Note that the log is not rotated in this version, so logging should only be enabled in debug purposes.

## QR codes
For use with QR code(s), an aditional call back function has to be declared, and sent as the last parameter to the ```SendRequest``` function. This call back function with then be called every second, for as long as the transaction is outstanding, with a byte array in PNG format containing a QR code to display to the user.

Also note that the ```TokenStartRequred``` parameter must be set in the Auth/Sign requirements in order to enable the use of QR codes. Below is an example of how this could be done.

1. Define a call back function

```go
function myCallBack(sessionId, message, details string){
    fmt.Println("Session ID:", sessionId, " sent message:", message, " with the details:", details)
}
```
2. Define a call back function for receiving QR codes every second, to be displayed to the user
```go
function onQRCodeRenewal(pngImg []byte, sessionID string){
    fmt.Println("Session ID:", sessionId, "received data for an updated QR code")
    // Update the display/page with the received pngImg data
}
```
3. Create an instance of the bankid.Connection struct with the call back function as argument.
```go
conn := bankid.New("", myCallBack)
defer bankid.Close
```
4. For each request from the client, call the bankid.Connection.SendRequest method. Note that the remote client's IP address must be provided as the first argument, and to enable the use of QR code(s) a pointer to a ```Requirements``` struct and a ```FOnNewQRCode``` call back function is also provided as arguments.
```go
sessionID := conn.SendRequest("192.168.0.1", "", "", &bankid.Requirements{TokenStartReqired: true}, onQRCodeRenewal)
```

Now, at every status update of the request the call back function ```myCallBack``` is called just as before, allowing for handling the session accordingly, but as long as this request is outstanding, the call back function ```onQRCodeRenewal``` will also be called every second, providing a PNG formatted byte array to be displayed for the user.

## Formatted text to sign
The support for formatted ```userVisibleData``` in the BankID RPv5.1 specifications is not yet implemented in this library.

## Auth/Sign requirements
Specific requirements may be needed at Auth or Sign requests. These requirements can be provided as a pointer to a ```Requirement``` struct as an argument to the ```SendRequest``` method. The different members of the struct are briefly described below. For more information about the different requirements, plase see the [official documentation](https://www.bankid.com/rp/info).

### ```PersonalNumber```
If the user is authenticating by providing his/her personal number then that personal number is set through this member. It has to be a 12 digit correct Swedish personal number.

### ```UserNoneVisibleData```
Data not visible to the user can be provided as part of the BankID signature, signed by the user. This data must be Base64 encoded and max 40.000 characters after encoding.

### ```CardReader```
If the user is required to use a card reader, this member can be set to either ```class1``` to force usage of at least a transparent card readers (where the PIN code is entered using the computer's key pad) or ```class2``` requiring the use of a key pad provided card reader. Please note that ```CertificatePolicies``` should be used iin conjunction with this member to avoid undefined behavior.

### ```CertificatePolicies```
This member can be used to force usage of the diffent types of BankID (file based, mobile phone based or smart card based). Please see the [official documentation](https://www.bankid.com/rp/info) for more information.

### ```IssuerCN```
Not needed in normal usage, please see the [official documentation](https://www.bankid.com/rp/info) for more information.

### ```TokenStartRequired```
This can be set to ```true``` either to be able to autostart the BankID App on the same device, through app switching using the ```autoStartToken```, or to allow the usage of QR codes. This library only supports usage of "animated", or continuously updated, QR codes. See ```FOnNewQRCode``` for more information.

### ```AllowFingerprint```
If set to ```true``` users of iOS and Android devices may use fingerprint for authentication and signing, if the device supports it, if the user has configured the device to use it, and if the user has configured BankID to use it.

## Example usage
See the [example folder](https://github.com/hossner/bankid/tree/master/example).


## BankID appapi v5 documentation
See [https://bankid.com/rp/info](https://www.bankid.com/rp/info) for more information about the appapi.

