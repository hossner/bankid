package bankid

// Package bankid provide structs and methods to access the Swedish BankID service through the v.5 appapi.

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io/ioutil"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/xid"

	"github.com/hossner/bankid/internal/config"
	"golang.org/x/crypto/pkcs12"
)

const (
	version          = "0.1"
	internalErrorMsg = "error"
	letterBytes      = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

var connection *Connection

/*
=========================================================================================
====================================== Exported =========================================
=========================================================================================
*/

// Connection holds the session with the BankID server. Reused if multiple calls to 'New'
// is made.
type Connection struct {
	Version        string
	funcOnResponse FOnResponse
	cfg            *config.Config
	httpClient     *http.Client
	transQueues    map[string]chan byte
	orderRefs      map[string]string
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

// FOnResponse is used to return the response struct after a auth/sign request
// Returns: sessionID, status, message
type FOnResponse func(string, string, string)

// New returns a server connection. If a connection exists it is reused
func New(configFileName string, responseCallBack FOnResponse) (*Connection, error) {
	if connection != nil { // Reuse if multiple calls are made. No hot reload of change of config in this version
		return connection, nil
	}
	if responseCallBack == nil {
		return nil, errors.New("no call back function provided")
	}
	cfg, err := config.GetConfig(configFileName)
	if err != nil {
		return nil, err
	}
	cl, err := getHTTPClient(cfg)
	if err != nil {
		return nil, err
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
// otherwise it's an authentication request. Returns a session ID; the sessionID parameter if provided,
// otherwise a generated one
func (sc *Connection) SendRequest(endUserIP, sessionID, textToBeSigned string, requirements *Requirements) string {
	// If sessionID is empty string, a new session ID is generated
	if sessionID == "" {
		sessionID = xid.New().String()
	}
	// Todo: Check max length for sessionID (configurable?)
	ch := make(chan byte, 1)
	sc.transQueues[sessionID] = ch
	go sc.handleAuthSignRequest(endUserIP, textToBeSigned, sessionID, requirements, ch)
	return sessionID
}

// CancelRequest cancels an ongoing session
func (sc *Connection) CancelRequest(sessionID string) {
	if _, ex := sc.orderRefs[sessionID]; !ex {
		sc.funcOnResponse(sessionID, internalErrorMsg, "no session with provided ID")
		return
	}
	delete(sc.orderRefs, sessionID)
	sc.transQueues[sessionID] <- 1
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
func (sc *Connection) handleAuthSignRequest(endUserIP, textToBeSigned, sessionID string, requirements *Requirements, queue chan byte) {
	if ip := net.ParseIP(endUserIP); ip == nil {
		sc.funcOnResponse(sessionID, internalErrorMsg, "invalid IP address")
		return
	}
	// Todo: Validate that the sessionID is recognized
	if err := requirements.validate(textToBeSigned); err != nil {
		sc.funcOnResponse(sessionID, internalErrorMsg, err.Error())
		return
	}
	// Create and populate the auth/sign request going to the server...
	reqType, jsonStr, err := requestToJSON(endUserIP, textToBeSigned, sessionID, requirements)
	if err != nil {
		sc.funcOnResponse(sessionID, internalErrorMsg, err.Error())
		return
	}
	// Handle the initial request/response with the server...
	code, resp, err := sc.transmitRequest(reqType, jsonStr)
	if err != nil {
		sc.funcOnResponse(sessionID, internalErrorMsg, err.Error())
		return
	}
	if code != 200 {
		er, msg := handleServerError(code, resp)
		sc.funcOnResponse(sessionID, er, msg)
		return
	}
	var sr serverResponse // Should contain orderRef and autoStartToken
	err = json.Unmarshal(resp, &sr)
	if err != nil {
		sc.funcOnResponse(sessionID, internalErrorMsg, err.Error())
		return
	}
	// Return the autoStartToken to the caller...
	sc.funcOnResponse(sessionID, "sent", sr.AutoStartToken)
	or := sr.OrderRef
	sc.orderRefs[sessionID] = or
	// Start polling the server while status is pending
	sr.Status = "pending"
	sr.HintCode = ""
	// oldStat := sr.Status   // Should be ""
	oldHint := sr.HintCode // Should be ""
	// collecting := true
	// for collecting {
	for sr.Status == "pending" {
		select {
		case _ = <-queue: // Cancel requested...
			code, resp, err = sc.transmitRequest("cancel", []byte(`{"orderRef":"`+or+`"}`))
			if err != nil {
				sc.funcOnResponse(sessionID, internalErrorMsg, err.Error())
				return
			}
			if code != 200 {
				er, msg := handleServerError(code, resp)
				sc.funcOnResponse(sessionID, er, msg)
				return
			}
			delete(sc.transQueues, sessionID)
			sc.funcOnResponse(sessionID, "cancelled", "")
			// collecting = false
			return
		default:
			code, resp, err = sc.transmitRequest("collect", []byte(`{"orderRef":"`+or+`"}`))
			if err != nil {
				sc.funcOnResponse(sessionID, internalErrorMsg, err.Error())
				return
			}
			if code != 200 {
				er, msg := handleServerError(code, resp)
				sc.funcOnResponse(sessionID, er, msg)
				return
			}
			err = json.Unmarshal(resp, &sr)
			if err != nil {
				sc.funcOnResponse(sessionID, internalErrorMsg, err.Error())
				return
			}
			if sr.Status == "pending" {
				if sr.HintCode != oldHint {
					sc.funcOnResponse(sessionID, sr.HintCode, sr.Status)
					oldHint = sr.HintCode
				}
				time.Sleep(time.Duration(sc.cfg.PollDelay) * time.Millisecond)
			} else { // "failed" or "complete"
				sc.funcOnResponse(sessionID, sr.Status, sr.HintCode)
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
	resp, err := sc.httpClient.Do(req)

	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	// Here we need to parse the request to see if it was a 200 or an error, and to handle both the correct way
	return resp.StatusCode, body, nil
}

// validateRequirements parses through the caller provided Requirements struct and checks to
// verify that all parameters are correct. If so, a authSignRequestRequirements struct is
// filled and the pointer to that struct is returned
func (req *Requirements) validate(ttbs string) error {
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
	if req == nil {
		return nil
	}
	if _, err := strconv.Atoi(req.PersonalNumber); err != nil {
		return errors.New("parameter personalNumber malformed")
	}
	if len(ttbs) > 40000 {
		return errors.New("parameter userVisibleData data too long")
	}
	if req == nil {
		return nil
	}
	if len(req.UserNonVisibleData) > 200000 {
		return errors.New("parameter userNonVisibleData data too long")
	}
	if len(req.PersonalNumber) > 0 && len(req.PersonalNumber) != 12 {
		return errors.New("parameter personalNumber must be 12 digits long")
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
	SessionID          string        `json:"-"`
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
			Cert           struct {
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
func requestToJSON(endUserIP, textToBeSigned, sessionID string, requirements *Requirements) (string, []byte, error) {
	reqType := "auth"
	var req authSignRequest
	req.SessionID = sessionID
	req.EndUserIP = endUserIP
	req.UserVisibleData = textToBeSigned
	req.Requirement = requirements
	if requirements != nil {
		req.UserNonVisibleData = requirements.UserNonVisibleData
		if requirements.UserNonVisibleData != "" {
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
