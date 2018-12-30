package bankid

// Package bankid provide structs and methods to access the Swedish BankID service through the v.5 appapi.

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rs/xid"

	"github.com/hossner/bankid/internal/config"
	"golang.org/x/crypto/pkcs12"
)

const (
	version          = "0.1"
	internalErrorMsg = "error"
)

var connection *Connection

/*
=========================================================================================
====================================== Exported =========================================
=========================================================================================
*/

// Connection holds the connection with the BankID server. The same connection will be
// reused if multiple calls to 'New' are made.
type Connection struct {
	Version        string
	funcOnResponse FOnResponse
	cfg            *config.Config
	httpClient     *http.Client
	transQueues    map[string]chan byte
	orderRefs      map[string]string
	mu             sync.Mutex
}

// Requirements is used when specific requirements for the sign/auth request are needed.
type Requirements struct {
	PersonalNumber         string   `json:"-"`                    // 12 digits
	UserNonVisibleData     string   `json:"-"`                    // 40.000 bytes/chars
	CardReader             string   `json:"cardReader,omitempty"` //"class1" or "class2"
	CertificatePolicies    []string `json:"certificatePolicies,omitempty"`
	IssuerCN               []string `json:"issuerCn,omitempty"`
	AutoStartTokenRequired bool     `json:"autoStartTokenRequired,omitempty"`
	AllowFingerprint       bool     `json:"allowFingerprint,omitempty"`
}

// FOnResponse is the call back function used to return status updates after a auth/sign request has been made
// Returns: requestID, status, message
type FOnResponse func(requestID, status, message string)

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
		return nil, fmt.Errorf("could not get create configuration: %v", err)
	}
	cl, err := getHTTPClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("could not create an HTTP client: %v", err)
	}
	var sc Connection
	sc.Version = version
	sc.funcOnResponse = responseCallBack
	sc.cfg = cfg
	sc.httpClient = cl
	sc.transQueues = make(map[string]chan byte)
	sc.orderRefs = make(map[string]string)
	return &sc, nil
}

// SendRequest sends an auth/sign request to the BankID server. If textToBeSigned is provided it is a sign request,
// otherwise it's an authentication request. Returns a request ID; the same as the requestID parameter if provided,
// otherwise a generated one
func (sc *Connection) SendRequest(endUserIP, requestID, textToBeSigned string, requirements *Requirements) string {
	// If requestID is empty string, a new session ID is generated
	if requestID == "" {
		requestID = xid.New().String()
	}
	// Todo: Check max length for requestID (configurable?)
	ch := make(chan byte, 1)
	sc.transQueues[requestID] = ch
	go sc.handleAuthSignRequest(endUserIP, textToBeSigned, requestID, requirements, ch)
	return requestID
}

// CancelRequest cancels an ongoing session
func (sc *Connection) CancelRequest(requestID string) {
	if _, ex := sc.orderRefs[requestID]; !ex {
		sc.funcOnResponse(requestID, internalErrorMsg, "no session with provided ID")
		return
	}
	delete(sc.orderRefs, requestID)
	sc.transQueues[requestID] <- 1
}

// Close the Connection
func (sc *Connection) Close() {
	// Todo: Loop through sc.transQueues and cancel any ongoing requests...
}

/*
=========================================================================================
==================================== Connection =========================================
=========================================================================================
*/

// handleAuthSignRequest is called as a go routine. Veryfies the request and, if validated,
// transmits it to the server
// Todo: Break this method up in pieces...
func (sc *Connection) handleAuthSignRequest(endUserIP, textToBeSigned, requestID string, requirements *Requirements, queue chan byte) {
	if ip := net.ParseIP(endUserIP); ip == nil {
		sc.funcOnResponse(requestID, internalErrorMsg, "invalid IP address")
		return
	}
	if textToBeSigned != "" {
		if err := validateTTBS(textToBeSigned); err != nil {
			sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
			return
		}
	}
	if requirements != nil {
		if err := requirements.validate(); err != nil {
			sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
			return
		}
	}
	// Create and populate the auth/sign request going to the server...
	reqType, jsonStr, err := requestToJSON(endUserIP, textToBeSigned, requestID, requirements)
	if err != nil {
		sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
		return
	}
	// Handle the initial request/response with the server...
	code, resp, err := sc.transmitRequest(reqType, jsonStr)
	if err != nil {
		sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
		return
	}
	if code != 200 {
		er, msg := handleServerError(code, resp)
		sc.funcOnResponse(requestID, er, msg)
		return
	}
	var sr serverResponse // Should contain orderRef and autoStartToken
	err = json.Unmarshal(resp, &sr)
	if err != nil {
		sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
		return
	}
	// Return the autoStartToken to the caller...
	sc.funcOnResponse(requestID, "sent", sr.AutoStartToken)
	or := sr.OrderRef
	sc.orderRefs[requestID] = or
	// Start polling the server while status is pending
	sr.Status = "pending"
	sr.HintCode = ""
	oldHint := sr.HintCode // Should be ""
	for sr.Status == "pending" {
		select {
		case _ = <-queue: // Cancel requested...
			code, resp, err = sc.transmitRequest("cancel", []byte(`{"orderRef":"`+or+`"}`))
			if err != nil {
				sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
				return
			}
			if code != 200 {
				er, msg := handleServerError(code, resp)
				sc.funcOnResponse(requestID, er, msg)
				return
			}
			delete(sc.transQueues, requestID)
			sc.funcOnResponse(requestID, "cancelled", "")
			return
		default:
			code, resp, err = sc.transmitRequest("collect", []byte(`{"orderRef":"`+or+`"}`))
			if err != nil {
				sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
				return
			}
			if code != 200 {
				er, msg := handleServerError(code, resp)
				sc.funcOnResponse(requestID, er, msg)
				return
			}
			err = json.Unmarshal(resp, &sr)
			if err != nil {
				sc.funcOnResponse(requestID, internalErrorMsg, err.Error())
				return
			}
			switch sr.Status {
			case "pending":
				if sr.HintCode != oldHint {
					sc.funcOnResponse(requestID, sr.HintCode, sr.Status)
					oldHint = sr.HintCode
				}
				time.Sleep(time.Duration(sc.cfg.PollDelay) * time.Millisecond)
			case "failed": // "failed" or "complete"
				sc.funcOnResponse(requestID, sr.Status, sr.HintCode)
				return
			case "complete":
				sc.funcOnResponse(requestID, sr.Status, sr.CompletionData.User.Name)
				return
			default:
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
func (req *Requirements) validate() error {
	/*
	   type Requirements struct {
	   	PersonalNumber         string   `json:"-"`                    // 12 digits
	   	UserNonVisibleData     string   `json:"-"`                    // 40.000 bytes/chars
	   	CardReader             string   `json:"cardReader,omitempty"` //"class1" or "class2"
	   	CertificatePolicies    []string `json:"certificatePolicies,omitempty"`
	   	IssuerCN               []string `json:"issuerCn,omitempty"`
	   	AutoStartTokenRequired bool     `json:"autoStartTokenRequired,omitempty"`
	   	AllowFingerprint       bool     `json:"allowFingerprint,omitempty"`
	*/
	if _, err := strconv.Atoi(req.PersonalNumber); err != nil {
		return errors.New("parameter personalNumber malformed")
	}
	if len(req.PersonalNumber) > 0 && len(req.PersonalNumber) != 12 {
		return errors.New("parameter personalNumber must be 12 digits long")
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
	Details   string `json:"details"` // "alreadyInProgress", "invalidParameters", "unauthorized", "notFound", "requestTimeout", "unsupportedMediaType", "internalErrorMsg", "maintenance"
}

// requestToJSON takes the caller arguments, including ev. Requirements struct, and creates the JSON to be sent to the server
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
