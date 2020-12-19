package bankid

// Package bankid provide structs and methods to access the Swedish BankID service through the v.5 appapi.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/rs/xid"
	"github.com/skip2/go-qrcode"

	"github.com/hossner/bankid/internal/config"
	"golang.org/x/crypto/pkcs12"
)

const (
	version          = "0.1"
	internalErrorMsg = "error"
)

// The definition of log levels
const (
	DEBUG = iota
	INFO
	WARN
	ERROR
	FATAL
	PANIC
)

var logLevel = 0 // Loggin disabled by default
var logFile *os.File
var logLevels []string
var connection *Connection

// Connection holds the connection with the BankID server. The same connection will be
// reused if multiple calls to 'New' are made.
type Connection struct {
	Version        string
	funcOnResponse FOnResponse
	cfg            *config.Config
	httpClient     *http.Client
	transQueues    map[string]chan byte
	orderRefs      map[string]string
	qrQuits        map[string]chan struct{}
	mu             sync.Mutex
}

// Requirements is used when specific requirements for the sign/auth request are needed.
type Requirements struct {
	PersonalNumber      string   `json:"-"`                    // 12 digits
	UserNonVisibleData  string   `json:"-"`                    // 40.000 bytes/chars
	CardReader          string   `json:"cardReader,omitempty"` //"class1" or "class2"
	CertificatePolicies []string `json:"certificatePolicies,omitempty"`
	IssuerCN            []string `json:"issuerCn,omitempty"`
	// AutoStartTokenRequired bool     `json:"autoStartTokenRequired,omitempty"`
	TokenStartRequired bool `json:"tokenStartRequired,omitempty"`
	AllowFingerprint   bool `json:"allowFingerprint,omitempty"`
}

// FOnResponse is the call back function used to return status updates after a auth/sign request has been made
// Returns: requestID, status, message
type FOnResponse func(requestID, status, message string)

// FOnNewQRCode is a call back function, used as an argument to SendRequest, that is called every second after
// the request, providing a new QR code
type FOnNewQRCode func(QRCode []byte, requestID string)

/*
=========================================================================================
==================================== Connection =========================================
=========================================================================================
*/

// New returns a server connection. If a connection allready exists, it will be reused
func New(configFileName string, responseCallBack FOnResponse) (*Connection, error) {
	if connection != nil { // Reuse if multiple calls are made. No hot reload of change of config in this version
		return connection, nil
	}
	if responseCallBack == nil {
		return nil, errors.New("no call back function provided")
	}
	cfg, err := config.New(configFileName)
	if err != nil {
		return nil, fmt.Errorf("could not create configuration: %v", err)
	}
	setupLoggin(cfg)
	cl, err := getHTTPClient(cfg)
	if err != nil {
		logprint(ERROR, "could not create an HTTP client:", err.Error())
		return nil, fmt.Errorf("could not create an HTTP client: %v", err)
	}
	var sc Connection
	sc.Version = version
	sc.funcOnResponse = responseCallBack
	sc.cfg = cfg
	sc.httpClient = cl
	sc.transQueues = make(map[string]chan byte)
	sc.orderRefs = make(map[string]string)
	sc.qrQuits = make(map[string]chan struct{})
	return &sc, nil
}

// SendRequest sends an auth/sign request to the BankID server. If textToBeSigned is provided it is a sign request,
// otherwise it's an authentication request. Returns a request ID; the same as the requestID parameter if provided,
// otherwise a generated one
func (sc *Connection) SendRequest(endUserIP, requestID, textToBeSigned string, requirements *Requirements, onQRCodeFunc FOnNewQRCode) string {
	if requestID == "" {
		requestID = xid.New().String()
		logprint(DEBUG, "requestID", requestID, "created")
	}
	logprint(DEBUG, requestID, ": new request to send")
	ch := make(chan byte, 1)
	sc.transQueues[requestID] = ch
	go sc.handleAuthSignRequest(endUserIP, textToBeSigned, requestID, requirements, ch, onQRCodeFunc)
	return requestID
}

// CancelRequest cancels an ongoing session
func (sc *Connection) CancelRequest(requestID string) {
	if _, ex := sc.orderRefs[requestID]; !ex {
		logprint(WARN, requestID, ": could not cancel requestID", requestID, " - not found")
		sc.funcOnResponse(requestID, internalErrorMsg, "no session with provided ID")
		return
	}
	delete(sc.orderRefs, requestID)
	sc.transQueues[requestID] <- 1
}

// Close the Connection
func (sc *Connection) Close() {
	// Todo: Loop through sc.transQueues and cancel any ongoing requests...
	logprint(DEBUG, "log closing")
	logFile.Close()
}

func validateParameters(endUserIP, textToBeSigned, requestID string, requirements *Requirements) string {
	if net.ParseIP(endUserIP) == nil {
		logprint(ERROR, requestID, ": could not validate IP address", endUserIP)
		return "invalid IP address: " + endUserIP
	}
	if textToBeSigned != "" {
		if err := validateTTBS(textToBeSigned); err != nil {
			logprint(ERROR, requestID, ": could not validate textToBeSigned:", err.Error())
			return err.Error()
		}
	}
	if requirements != nil {
		logprint(DEBUG, requestID, ": requirements struct provided")
		if err := validateRequirements(requirements); err != nil {
			logprint(ERROR, requestID, ": could not validate requirements:", err.Error())
			return err.Error()
		}
	}
	logprint(DEBUG, requestID, ": parameters validated")
	return ""
}

func (sc *Connection) generateQRCode(qr1, qr2, requestID string, fOnCode FOnNewQRCode) chan struct{} {
	if fOnCode == nil {
		return nil
	}

	/*
		qr1 = "67df3917-fa0d-44e5-b327-edcc928297f8"
		qr2 = "d28db9a7-4cde-429e-a983-359be676944c"
		nr := 0
		var png []byte
		h := hmac.New(sha256.New, []byte(qr2))
		h.Write([]byte(strconv.Itoa(nr)))
		png, _ = qrcode.Encode("bankid."+qr1+"."+strconv.Itoa(nr)+"."+hex.EncodeToString(h.Sum(nil)), qrcode.Low, -5)
		fOnCode(png, requestID)
		return nil
	*/
	nr := 0
	ticker := time.NewTicker(1 * time.Second)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				var png []byte
				h := hmac.New(sha256.New, []byte(qr2))
				h.Write([]byte(strconv.Itoa(nr)))
				png, err := qrcode.Encode("bankid."+qr1+"."+strconv.Itoa(nr)+"."+hex.EncodeToString(h.Sum(nil)), qrcode.Low, -5)
				if err != nil {
					logprint(ERROR, "", ": failed to generate QR code", err.Error())
					sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
				}
				fOnCode(png, requestID)
				nr++
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()
	return quit

}

func cancelQRCode(ch chan struct{}, fnq FOnNewQRCode) {
	if fnq != nil {
		close(ch)
	}
}

// handleAuthSignRequest is called as a go routine. Veryfies the request and, if validated,
// transmits it to the server
// Todo: Break this method up in pieces...
func (sc *Connection) handleAuthSignRequest(endUserIP, textToBeSigned, requestID string, requirements *Requirements, queue chan byte, onQRCodeFunc FOnNewQRCode) {
	if erMsg := validateParameters(endUserIP, textToBeSigned, requestID, requirements); erMsg != "" {
		sc.funcOnResponse(requestID, internalErrorMsg, erMsg)
		return
	}
	// Create and populate the auth/sign request going to the server...
	reqType, jsonStr, err := requestToJSON(endUserIP, textToBeSigned, requestID, requirements)
	if err != nil {
		logprint(ERROR, requestID, ": could not create JSON from request:", err.Error())
		sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
		return
	}
	// Handle the initial request/response with the server...
	code, resp, err := sc.transmitRequest(reqType, jsonStr)
	if err != nil {
		logprint(ERROR, requestID, ": failed to transmit request:", err.Error())
		sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
		return
	}
	if code != 200 {
		er, msg := handleServerError(code, resp)
		logprint(ERROR, requestID, ": received HTTP error", strconv.Itoa(code), ":", er, msg)
		sc.funcOnResponse(requestID, er, msg)
		return
	}
	var sr serverResponse // Should contain orderRef, autoStartToken, qrStartToken and qrStartSecret
	err = json.Unmarshal(resp, &sr)
	if err != nil {
		logprint(ERROR, requestID, ": failed to JSON decode server response:", err.Error())
		sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
		return
	}
	or := sr.OrderRef
	sc.orderRefs[requestID] = or
	sr.Status = "pending"
	sr.HintCode = ""
	oldHint := sr.HintCode // Should be ""
	sc.funcOnResponse(requestID, "sent", sr.AutoStartToken)
	if onQRCodeFunc != nil {
		sc.qrQuits[requestID] = sc.generateQRCode(sr.QRStartToken, sr.QRStartSecret, requestID, onQRCodeFunc)
	}
	for sr.Status == "pending" {
		select {
		case _ = <-queue: // Cancel requested...
			logprint(DEBUG, requestID, ": received cancel command")
			cancelQRCode(sc.qrQuits[requestID], onQRCodeFunc)
			code, resp, err = sc.transmitRequest("cancel", []byte(`{"orderRef":"`+or+`"}`))
			if err != nil {
				logprint(ERROR, requestID, ": failed to send cancel request to server:", err.Error())
				sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
				return
			}
			if code != 200 {
				er, msg := handleServerError(code, resp)
				logprint(ERROR, requestID, ": received HTTP error", strconv.Itoa(code), ":", er, msg)
				sc.funcOnResponse(requestID, er, msg)
				return
			}
			delete(sc.transQueues, requestID)
			logprint(DEBUG, requestID, ": cancelled")
			sc.funcOnResponse(requestID, "cancelled", "")
			return
		default:
			code, resp, err = sc.transmitRequest("collect", []byte(`{"orderRef":"`+or+`"}`))
			if err != nil {
				logprint(ERROR, requestID, ": failed to send collect request to server:", err.Error())
				cancelQRCode(sc.qrQuits[requestID], onQRCodeFunc)
				sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
				return
			}
			if code != 200 {
				er, msg := handleServerError(code, resp)
				cancelQRCode(sc.qrQuits[requestID], onQRCodeFunc)
				logprint(ERROR, requestID, ": received HTTP error", strconv.Itoa(code), ":", er, msg)
				sc.funcOnResponse(requestID, er, msg)
				return
			}
			err = json.Unmarshal(resp, &sr)
			if err != nil {
				logprint(ERROR, requestID, ": failed to JSON decode server response:", err.Error())
				cancelQRCode(sc.qrQuits[requestID], onQRCodeFunc)
				sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
				return
			}
			switch sr.Status {
			case "pending":
				if sr.HintCode != oldHint {
					logprint(DEBUG, requestID, ": status changed to", sr.HintCode)
					sc.funcOnResponse(requestID, sr.HintCode, sr.Status)
					oldHint = sr.HintCode
				}
				time.Sleep(time.Duration(sc.cfg.PollDelay) * time.Millisecond)
			case "failed": // "failed" or "complete"
				logprint(DEBUG, requestID, ": status changed to", sr.HintCode)
				cancelQRCode(sc.qrQuits[requestID], onQRCodeFunc)
				sc.funcOnResponse(requestID, sr.Status, sr.HintCode)
				return
			case "complete":
				logprint(DEBUG, requestID, ": status changed to", sr.HintCode)
				cancelQRCode(sc.qrQuits[requestID], onQRCodeFunc)
				sc.funcOnResponse(requestID, sr.Status, sr.CompletionData.User.Name)
				return
			default:
				logprint(DEBUG, requestID, ": unknown status", sr.Status, "in response from server")
				cancelQRCode(sc.qrQuits[requestID], onQRCodeFunc)
				sc.funcOnResponse(requestID, internalErrorMsg, "unknown status in response from server")
				return
			}
		}
	}
}

// transmitRequest handles the communication with the server
// Returns HTTP response code, HTTP body and an error
func (sc *Connection) transmitRequest(reqType string, jsonStr []byte) (int, []byte, error) {
	req, err := http.NewRequest("POST", sc.cfg.ServiceURL+"/"+reqType, bytes.NewBuffer(jsonStr))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Host", sc.cfg.HTTPClientConfig.RequestHeader.Host)
	req.Header.Set("Content-Type", sc.cfg.HTTPClientConfig.RequestHeader.ContentType)
	sc.mu.Lock()
	resp, err := sc.httpClient.Do(req)
	defer sc.mu.Unlock()
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	bd, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, bd, nil
}

// validateRequirements parses through the caller provided Requirements struct and checks to
// verify that all parameters are correct. If so, a authSignRequestRequirements struct is
// filled and the pointer to that struct is returned
func validateRequirements(req *Requirements) error {
	if len(req.PersonalNumber) > 0 {
		if _, err := strconv.Atoi(req.PersonalNumber); err != nil {
			return errors.New("parameter personalNumber malformed")
		}
		if len(req.PersonalNumber) > 0 && len(req.PersonalNumber) != 12 {
			return errors.New("parameter personalNumber must be 12 digits long")
		}
	}
	if len(req.UserNonVisibleData) > 200000 {
		return errors.New("parameter userNonVisibleData data too long")
	}
	if len(req.CardReader) > 0 && req.CardReader != "class1" && req.CardReader != "class2" {
		return errors.New("parameter cardReader set to invalid value")
	}
	// Todo: Validate CertificatePolicies and IssuerCN
	return nil
}

/*
// ================================================================================================
*/

// authSignRequest is an internal structure to hold the auth/sign request, which is converted
// to a JSON string before sent to the server
type authSignRequest struct {
	RequestID          string        `json:"-"`
	PersonalNumber     string        `json:"personalNumber,omitempty"`     // 12 digits
	EndUserIP          string        `json:"endUserIp"`                    // IPv4 or IPv6 format
	UserVisibleData    string        `json:"userVisibleData,omitempty"`    // 2.000 bytes/chars
	UserNonVisibleData string        `json:"userNonVisibleData,omitempty"` // 40.000 bytes/chars
	Requirement        *Requirements `json:"requirement,omitempty"`
}

type serverResponse struct {
	AutoStartToken string `json:"autoStartToken,omitempty"` // Format: "131daac9-16c6-4618-beb0-365768f37288"
	QRStartToken   string `json:"qrStartToken,omitempty"`
	QRStartSecret  string `json:"qrStartSecret,omitempty"`
	OrderRef       string `json:"orderRef,omitempty"`
	Status         string `json:"status"`
	HintCode       string `json:"hintCode,omitempty"`
	CompletionData struct {
		User struct {
			PersonalNumber string `json:"personalNumber"`
			Name           string `json:"name"`
			GivenName      string `json:"givenName"`
			Surname        string `json:"surname"`
			Device         struct {
				IPAddress string `json:"ipAddress,omitempty"`
			} `json:"device,omitempty"`
			Cert struct {
				NotBefore string `json:"notBefore"`
				NotAfter  string `json:"notAfter"`
			} `json:"cert"`
			Signature    string `json:"signature"`
			OSCPResponse string `json:"ocspResponse"`
		} `json:"user,omitempty"`
	} `json:"completionData,omitempty"`
}

type serverError struct {
	ErrorCode string `json:"errorCode"`
	Details   string `json:"details"`
}

func requestToJSON(endUserIP, textToBeSigned, requestID string, requirements *Requirements) (string, []byte, error) {
	reqType := "auth"
	var req authSignRequest
	req.RequestID = requestID
	req.EndUserIP = endUserIP
	req.UserVisibleData = textToBeSigned
	req.Requirement = requirements
	if requirements != nil {
		if requirements.UserNonVisibleData != "" {
			req.UserNonVisibleData = requirements.UserNonVisibleData
			reqType = "sign"
		}
		req.PersonalNumber = requirements.PersonalNumber
	}
	json, err := json.Marshal(req)
	return reqType, json, err
}

func handleServerError(code int, resp []byte) (string, string) {
	var se serverError
	if err := json.Unmarshal(resp, &se); err != nil {
		return internalErrorMsg, err.Error()
	}
	return se.ErrorCode, se.Details
}

// Initialize a http.Client
func getHTTPClient(cfg *config.Config) (*http.Client, error) {
	tlsCfg, err := getTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{TLSClientConfig: tlsCfg}
	return &http.Client{Transport: tr}, nil
}

// Initialize a tls.Config struct based on the client and server certs
func getTLSConfig(cfg *config.Config) (*tls.Config, error) {
	// Todo: Handle case where P12 is split into cert and key file
	p12, err := ioutil.ReadFile(cfg.GetFilePath("userP12FileName"))
	if err != nil {
		return nil, err
	}
	blocks, err := pkcs12.ToPEM(p12, cfg.CertStore.UserPrivateKeyPassword)
	if err != nil {
		return nil, err
	}
	var pemData []byte
	for _, b := range blocks {
		pemData = append(pemData, pem.EncodeToMemory(b)...)
	}
	cert, err := tls.X509KeyPair(pemData, pemData)
	if err != nil {
		return nil, err
	}

	// Handle the CA certificate
	ca, err := ioutil.ReadFile(cfg.GetFilePath("caCertFileName"))
	if err != nil {
		return nil, err
	}
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(ca) {
		return nil, errors.New("Failed appending certs")
	}

	tlsCfg := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		ClientCAs:          certPool,
		InsecureSkipVerify: true, // <- This to accept the self-signed CA cert
	}
	return tlsCfg, nil
}

func validateTTBS(ttbs string) error {
	// TODO: Validate that ttbs is valid Base64
	if len(ttbs) > 40000 {
		return errors.New("parameter userVisibleData data too long")
	}
	return nil
}

func setupLoggin(cfg *config.Config) {
	logLevel = cfg.LogLevel
	logLevels = cfg.LogPrefixes
	log.SetOutput(os.Stderr)
	if cfg.LogLevel < 1 {
		return
	}
	if cfg.LogFileName != "" {
		lf, err := os.OpenFile(cfg.GetFilePath("logFile"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			logprint(ERROR, "could not open log file", cfg.GetFilePath("logFile"), ":", err.Error())
			return
		}
		logFile = lf
		log.SetOutput(lf)
		logprint(DEBUG, "log started")
	}
}

func logprint(lvl int, a ...string) {
	if logLevel < 1 || lvl+1 < logLevel || lvl < 0 {
		return
	}
	if lvl >= len(logLevels) {
		lvl = len(logLevels)
		log.Println("ERROR: missing log level prefixes in config file!")
	}
	if lvl < 0 {
		log.Println("ERROR:", a)
		return
	}
	log.Println(logLevels[lvl], a)
}
