package ltiservice

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Edgems-Math/go-lti/lti"
	jwtmiddleware "github.com/auth0/go-jwt-middleware"
	jwt "github.com/dgrijalva/jwt-go"
	"github.com/pkg/errors"
)

// The name of the property in the request where the user information
// from the JWT will be stored.
const userProperty = "user"

// GetLaunchHandler Returns a handler for a LaunchMessage
// Once the incoming JWT is decoded and validated, the provided callback function will
// be executed
func (ltis *LTIService) GetLaunchHandler(callback func(lti.LaunchMessage)) http.Handler {
	handlerFunc := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ltis.launch(w, req, callback)
	})

	//Wraps the handler with middleware that decodes the incoming JWT
	opts := jwtmiddleware.Options{
		SigningMethod: jwt.SigningMethodRS256,
		UserProperty:  userProperty,
		Extractor: func(r *http.Request) (string, error) {
			return r.FormValue("id_token"), nil
		},
		Debug:               true,
		ValidationKeyGetter: ltis.getValidationKey,
		ErrorHandler:        tokenMWErrorHandler,
	}
	jwtMW := jwtmiddleware.New(opts)

	return jwtMW.Handler(handlerFunc)
}

func (ltis *LTIService) launch(w http.ResponseWriter, req *http.Request, callback func(lti.LaunchMessage)) {
	//Extract claims from the JWT
	userToken := req.Context().Value(userProperty)
	tok := userToken.(*jwt.Token)
	claims := tok.Claims.(jwt.MapClaims)

	//Validate state
	if err := ltis.validateState(req); err != nil {
		http.Error(w, err.Error(), 401)
		return
	}

	//Validate nonce
	//disabled for now because apparently it doesn't work anyway

	//Validate client ID
	if err := ltis.validateClientID(claims); err != nil {
		http.Error(w, err.Error(), 401)
		return
	}

	//Validate deployment
	if err := ltis.validateDeployment(claims); err != nil {
		http.Error(w, err.Error(), 401)
		return
	}

	//Validate message
	if err := ltis.validateMessage(claims); err != nil {
		http.Error(w, err.Error(), 401)
		return
	}

	//Validate timing
	if err := ltis.validateTiming(claims); err != nil {
		http.Error(w, err.Error(), 401)
		return
	}

	// launchMessage, err := lti.ParseLaunchMessage(claims)
	launchMessage, err := lti.ParseLaunchMessage(claims)
	if err != nil {
		http.Error(w, err.Error(), 401)
	}

	callback(launchMessage)
}

// tokenMWErrorHandler provided to the JWT middleware for it to handle errors
func tokenMWErrorHandler(w http.ResponseWriter, r *http.Request, err string) {
	http.Error(w, fmt.Sprintf("Token issue: %s", err), 401)
}

func (ltis *LTIService) validateState(req *http.Request) error {
	stateVal := req.FormValue("state")
	cookieName := fmt.Sprintf("mzdevinc_lti_go_%s", stateVal)
	stateCookie, err := req.Cookie(cookieName)

	cookieName2 := fmt.Sprintf("mzdevinc_lti_go2_%s", stateVal)
	stateCookie2, err2 := req.Cookie(cookieName2)

	if err != nil && err2 != nil {
		return errors.Wrap(err, "Missing authentication cookie\nPlease ensure that your browser is not blocking cookies\nError")
	}
	if (stateCookie == nil || stateCookie.Value == "") && (stateCookie2 == nil || stateCookie2.Value == "") {
		return fmt.Errorf("Empty state cookie in request")
	}
	if (stateCookie == nil || stateCookie.Value != stateVal) && (stateCookie2 == nil || stateCookie2.Value != stateVal) {
		return fmt.Errorf("State not found")
	}
	return nil
}

// func (ltis *LTIService) validateNonce(req *http.Request, nonce string) error {
// 	nonceOk := cache.CheckNonce(req, nonce)
// 	if nonceOk {
// 		return nil
// 	}
// 	ltis.debug("nonce check failed")
// 	// platform is never sending the right nonce.
// 	//  It's commented out in the php reference: https://github.com/IMSGlobal/lti-1-3-php-library/blob/1535dc1689121e37a18d843156fa449383255107/src/lti/lti_message_launch.php#L258
// 	//  for now, skip the error. Maybe this will be fixed in the future.
// 	// return fmt.Errorf("Invalid Nonce")
// 	return nil
// }

func (ltis *LTIService) validateClientID(claims jwt.MapClaims) error {
	var aud string
	var audClaim interface{} = claims["aud"]
	switch v := audClaim.(type) {
	case string:
		aud = v
	case []string:
		aud = v[0]
	default:
		return fmt.Errorf("aud claim is unexpected type: %T", v)
	}
	// check that the clientIds match
	if ltis.Config.ClientID != aud {
		return fmt.Errorf("ClientId does not match issuer registration")
	}
	return nil
}

func (ltis *LTIService) validateDeployment(claims jwt.MapClaims) error {
	depID, ok := claims["https://purl.imsglobal.org/spec/lti/claim/deployment_id"]
	if !ok {
		return fmt.Errorf("No deployment ID")
	}
	depIDStr := depID.(string)
	if depIDStr == "" {
		return fmt.Errorf("No deployment ID")
	}

	return nil
}

func (ltis *LTIService) validateMessage(claims jwt.MapClaims) error {
	msgType, ok := claims["https://purl.imsglobal.org/spec/lti/claim/message_type"]
	if !ok {
		return fmt.Errorf("Empty message type not allowed")
	}
	msgTypeStr := msgType.(string)

	switch msgTypeStr {
	case "":
		return fmt.Errorf("Empty message type not allowed")
	case "LtiResourceLinkRequest":
		return validateMessageTypeLinkRequest(claims)
	case "LtiDeepLinkingRequest":
		return validateMessageTypeDeepLink(claims)
	default:
		return fmt.Errorf("unknown message type (%q)", msgType)
	}
}

func (ltis *LTIService) validateTiming(claims jwt.MapClaims) error {
	iatRaw, ok := claims["iat"]
	if !ok {
		return fmt.Errorf("Token creation time is missing")
	}
	iat := iatRaw.(float64)

	expRaw, ok := claims["exp"]
	if !ok {
		return fmt.Errorf("Token expiration time is missing")
	}
	exp := expRaw.(float64)

	currentTime := float64(time.Now().Unix())
	if iat-1 > currentTime {
		return fmt.Errorf("Token creation time is invalid")
	}
	if currentTime > exp {
		return fmt.Errorf("Token is expired")
	}

	return nil
}

func validateMessageTypeLinkRequest(claims jwt.MapClaims) error {
	if err := validateMessageTypeCommon(claims); err != nil {
		return err
	}

	rlMap, ok := claims["https://purl.imsglobal.org/spec/lti/claim/resource_link"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("resource link claim is missing")
	}
	if id, ok := rlMap["id"]; !ok || id.(string) == "" {
		return fmt.Errorf("resource link id is missing")
	}
	if targetLinkURI, ok := claims["https://purl.imsglobal.org/spec/lti/claim/target_link_uri"]; !ok || targetLinkURI.(string) == "" {
		return fmt.Errorf("target link URI is missing")
	}
	return nil
}

func validateMessageTypeDeepLink(claims jwt.MapClaims) error {
	if err := validateMessageTypeCommon(claims); err != nil {
		return err
	}

	dlsMap, ok := claims["https://purl.imsglobal.org/spec/lti-dl/claim/deep_linking_settings"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("deep link settings claim is missing")
	}
	if deepLinkReturnURL, ok := dlsMap["deep_link_return_url"]; !ok || deepLinkReturnURL.(string) == "" {
		return fmt.Errorf("deep link return url is missing")
	}

	return nil
}

// validateMessageTypeCommon checks for claims that should be part of any message type
func validateMessageTypeCommon(claims jwt.MapClaims) error {
	if sub, ok := claims["sub"]; !ok || sub.(string) == "" {
		return fmt.Errorf("token is missing user (sub) claim")
	}
	if version, ok := claims["https://purl.imsglobal.org/spec/lti/claim/version"]; !ok || version.(string) != "1.3.0" {
		return fmt.Errorf("token has incompatible lti version")
	}
	if roles, ok := claims["https://purl.imsglobal.org/spec/lti/claim/roles"]; !ok || roles == nil {
		return fmt.Errorf("token is missing roles claim")
	}
	return nil
}

func isDeepLinkLaunch(claims jwt.MapClaims) bool {
	msgType := claims["https://purl.imsglobal.org/spec/lti/claim/message_type"].(string)
	return msgType == "LtiDeepLinkingRequest"
}

func isResourceLaunch(claims jwt.MapClaims) bool {
	msgType := claims["https://purl.imsglobal.org/spec/lti/claim/message_type"].(string)
	return msgType == "LtiResourceLinkRequest"
}
