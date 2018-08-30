package dnsfilter

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluele/gcache"
	"golang.org/x/net/publicsuffix"
)

const defaultCacheSize = 64 * 1024 // in number of elements
const defaultCacheTime time.Duration = 30 * time.Minute

const defaultHTTPTimeout time.Duration = 5 * time.Minute
const defaultHTTPMaxIdleConnections = 100

const defaultSafebrowsingServer = "sb.adtidy.org"
const defaultSafebrowsingURL = "http://%s/safebrowsing-lookup-hash.html?prefixes=%s"
const defaultParentalURL = "http://pctrl.adguard.com/check-parental-control-hash?prefixes=%s&sensitivity=%d"

var ErrInvalidSyntax = errors.New("dnsfilter: invalid rule syntax")
var ErrInvalidParental = errors.New("dnsfilter: invalid parental sensitivity, must be either 3, 10, 13 or 17")

const shortcutLength = 6 // used for rule search optimization, 6 hits the sweet spot

const enableFastLookup = true         // flag for debugging, must be true in production for faster performance
const enableDelayedCompilation = true // flag for debugging, must be true in production for faster performance

type Config struct {
	safeSearchEnabled   bool
	safeBrowsingEnabled bool
	safeBrowsingServer  string
	parentalEnabled     bool
	parentalSensitivity int // must be either 3, 10, 13 or 17
}

type Rule struct {
	text         string // text without @@ decorators or $ options
	shortcut     string // for speeding up lookup
	originalText string // original text for reporting back to applications

	// options
	options []string // optional options after $

	// parsed options
	isWhitelist bool
	isImportant bool
	apps        []string

	// user-supplied data
	listID uint32

	// compiled regexp
	compiled *regexp.Regexp

	sync.RWMutex
}

type LookupStats struct {
	Requests   uint64 // number of HTTP requests that were sent
	CacheHits  uint64 // number of lookups that didn't need HTTP requests
	Pending    int64  // number of currently pending HTTP requests
	PendingMax int64  // maximum number of pending HTTP requests
}

type Stats struct {
	Safebrowsing LookupStats
	Parental     LookupStats
}

// Dnsfilter holds added rules and performs hostname matches against the rules
type Dnsfilter struct {
	storage      map[string]*Rule // rule storage, not used for matching, needs to be key->value
	storageMutex sync.RWMutex

	// rules are checked against these lists in the order defined here
	important *rulesTable // more important than whitelist and is checked first
	whiteList *rulesTable // more important than blacklist
	blackList *rulesTable

	// HTTP lookups for safebrowsing and parental
	client    http.Client     // handle for http client -- single instance as recommended by docs
	transport *http.Transport // handle for http transport used by http client

	config Config
}

//go:generate stringer -type=Reason

// filtered/notfiltered reason
type Reason int

const (
	// reasons for not filtering
	NotFilteredNotFound  Reason = iota // host was not find in any checks, default value for result
	NotFilteredWhiteList               // the host is explicitly whitelisted
	NotFilteredError                   // there was a transitive error during check

	// reasons for filtering
	FilteredBlackList    // the host was matched to be advertising host
	FilteredSafeBrowsing // the host was matched to be malicious/phishing
	FilteredParental     // the host was matched to be outside of parental control settings
	FilteredInvalid      // the request was invalid and was not processed
	FilteredSafeSearch   // the host was replaced with safesearch variant
)

// these variables need to survive coredns reload
var (
	stats             Stats
	safebrowsingCache = gcache.New(defaultCacheSize).LRU().Expiration(defaultCacheTime).Build()
	parentalCache     = gcache.New(defaultCacheSize).LRU().Expiration(defaultCacheTime).Build()
)

// search result
type Result struct {
	IsFiltered bool
	Reason     Reason
	Rule       string
}

func (r Reason) Matched() bool {
	return r != NotFilteredNotFound
}

// CheckHost tries to match host against rules, then safebrowsing and parental if they are enabled
func (d *Dnsfilter) CheckHost(host string) (Result, error) {
	// sometimes DNS clients will try to resolve ".", which in turns transforms into "" when it reaches here
	if host == "" {
		return Result{Reason: FilteredInvalid}, nil
	}

	// try filter lists first
	result, err := d.matchHost(host)
	if err != nil {
		return result, err
	}
	if result.Reason.Matched() {
		return result, nil
	}

	// check safebrowsing if no match
	if d.config.safeBrowsingEnabled {
		result, err = d.checkSafeBrowsing(host)
		if err != nil {
			// failed to do HTTP lookup -- treat it as if we got empty response, but don't save cache
			log.Printf("Failed to do safebrowsing HTTP lookup, ignoring check: %v", err)
			return Result{}, nil
		}
		if result.Reason.Matched() {
			return result, nil
		}
	}

	// check parental if no match
	if d.config.parentalEnabled {
		result, err = d.checkParental(host)
		if err != nil {
			// failed to do HTTP lookup -- treat it as if we got empty response, but don't save cache
			log.Printf("Failed to do parental HTTP lookup, ignoring check: %v", err)
			return Result{}, nil
		}
		if result.Reason.Matched() {
			return result, nil
		}
	}

	// nothing matched, return nothing
	return Result{}, nil
}

//
// rules table
//

type rulesTable struct {
	rulesByShortcut map[string][]*Rule
	rulesLeftovers  []*Rule
	sync.RWMutex
}

func newRulesTable() *rulesTable {
	return &rulesTable{
		rulesByShortcut: make(map[string][]*Rule),
		rulesLeftovers:  make([]*Rule, 0),
	}
}

func (r *rulesTable) Add(rule *Rule) {
	r.Lock()
	if len(rule.shortcut) == shortcutLength && enableFastLookup {
		r.rulesByShortcut[rule.shortcut] = append(r.rulesByShortcut[rule.shortcut], rule)
	} else {
		r.rulesLeftovers = append(r.rulesLeftovers, rule)
	}
	r.Unlock()
}

func (r *rulesTable) matchByHost(host string) (Result, error) {
	res, err := r.searchShortcuts(host)
	if err != nil {
		return res, err
	}
	if res.Reason.Matched() {
		return res, nil
	}

	res, err = r.searchLeftovers(host)
	if err != nil {
		return res, err
	}
	if res.Reason.Matched() {
		return res, nil
	}

	return Result{}, nil
}

func (r *rulesTable) searchShortcuts(host string) (Result, error) {
	// check in shortcuts first
	for i := 0; i < len(host); i++ {
		shortcut := host[i:]
		if len(shortcut) > shortcutLength {
			shortcut = shortcut[:shortcutLength]
		}
		if len(shortcut) != shortcutLength {
			continue
		}
		rules, ok := r.rulesByShortcut[shortcut]
		if !ok {
			continue
		}
		for _, rule := range rules {
			res, err := rule.match(host)
			// error? stop search
			if err != nil {
				return res, err
			}
			// matched? stop search
			if res.Reason.Matched() {
				return res, err
			}
			// continue otherwise
		}
	}
	return Result{}, nil
}

func (r *rulesTable) searchLeftovers(host string) (Result, error) {
	for _, rule := range r.rulesLeftovers {
		res, err := rule.match(host)
		// error? stop search
		if err != nil {
			return res, err
		}
		// matched? stop search
		if res.Reason.Matched() {
			return res, err
		}
		// continue otherwise
	}
	return Result{}, nil
}

func findOptionIndex(text string) int {
	for i, r := range text {
		// ignore non-$
		if r != '$' {
			continue
		}
		// ignore `\$`
		if i > 0 && text[i-1] == '\\' {
			continue
		}
		// ignore `$/`
		if i > len(text) && text[i+1] == '/' {
			continue
		}
		return i + 1
	}
	return -1
}

func (rule *Rule) extractOptions() error {
	optIndex := findOptionIndex(rule.text)
	if optIndex == 0 { // starts with $
		return ErrInvalidSyntax
	}
	if optIndex == len(rule.text) { // ends with $
		return ErrInvalidSyntax
	}
	if optIndex < 0 {
		return nil
	}

	optionsStr := rule.text[optIndex:]
	rule.text = rule.text[:optIndex-1] // remove options from text

	begin := 0
	i := 0
	for i = 0; i < len(optionsStr); i++ {
		switch optionsStr[i] {
		case ',':
			if i > 0 {
				// it might be escaped, if so, ignore
				if optionsStr[i-1] == '\\' {
					break // from switch, not for loop
				}
			}
			rule.options = append(rule.options, optionsStr[begin:i])
			begin = i + 1
		}
	}
	if begin != i {
		// there's still an option remaining
		rule.options = append(rule.options, optionsStr[begin:])
	}

	return nil
}

func (rule *Rule) parseOptions() error {
	err := rule.extractOptions()
	if err != nil {
		return err
	}

	for _, option := range rule.options {
		switch {
		case option == "important":
			rule.isImportant = true
		case strings.HasPrefix(option, "app="):
			option = strings.TrimPrefix(option, "app=")
			rule.apps = strings.Split(option, "|")
		default:
			return ErrInvalidSyntax
		}
	}

	return nil
}

func (rule *Rule) extractShortcut() {
	// regex rules have no shortcuts
	if rule.text[0] == '/' && rule.text[len(rule.text)-1] == '/' {
		return
	}

	fields := strings.FieldsFunc(rule.text, func(r rune) bool {
		switch r {
		case '*', '^', '|':
			return true
		}
		return false
	})
	longestField := ""
	for _, field := range fields {
		if len(field) > len(longestField) {
			longestField = field
		}
	}
	if len(longestField) > shortcutLength {
		longestField = longestField[:shortcutLength]
	}
	rule.shortcut = strings.ToLower(longestField)
}

func (rule *Rule) compile() error {
	rule.RLock()
	isCompiled := rule.compiled != nil
	rule.RUnlock()
	if isCompiled {
		return nil
	}

	expr, err := ruleToRegexp(rule.text)
	if err != nil {
		return err
	}

	compiled, err := regexp.Compile(expr)
	if err != nil {
		return err
	}

	rule.Lock()
	rule.compiled = compiled
	rule.Unlock()

	return nil
}

func (rule *Rule) match(host string) (Result, error) {
	res := Result{}
	err := rule.compile()
	if err != nil {
		return res, err
	}
	rule.RLock()
	matched := rule.compiled.MatchString(host)
	rule.RUnlock()
	if matched {
		res.Reason = FilteredBlackList
		res.IsFiltered = true
		if rule.isWhitelist {
			res.Reason = NotFilteredWhiteList
			res.IsFiltered = false
		}
		res.Rule = rule.text
	}
	return res, nil
}

func getCachedReason(cache gcache.Cache, host string) (result Result, isFound bool, err error) {
	isFound = false // not found yet

	// get raw value
	rawValue, err := cache.Get(host)
	if err == gcache.KeyNotFoundError {
		// not a real error, just not found
		err = nil
		return
	}
	if err != nil {
		// real error
		return
	}

	// since it can be something else, validate that it belongs to proper type
	cachedValue, ok := rawValue.(Result)
	if ok == false {
		// this is not our type -- error
		text := "SHOULD NOT HAPPEN: entry with invalid type was found in lookup cache"
		log.Println(text)
		err = errors.New(text)
		return
	}
	isFound = ok
	return cachedValue, isFound, err
}

// for each dot, hash it and add it to string
func hostnameToHashParam(host string, addslash bool) (string, map[string]bool) {
	var hashparam bytes.Buffer
	hashes := map[string]bool{}
	tld, icann := publicsuffix.PublicSuffix(host)
	if icann == false {
		// private suffixes like cloudfront.net
		tld = ""
	}
	curhost := host
	for {
		if curhost == "" {
			// we've reached end of string
			break
		}
		if tld != "" && curhost == tld {
			// we've reached the TLD, don't hash it
			break
		}
		tohash := []byte(curhost)
		if addslash {
			tohash = append(tohash, '/')
		}
		sum := sha256.Sum256(tohash)
		hexhash := fmt.Sprintf("%X", sum)
		hashes[hexhash] = true
		hashparam.WriteString(fmt.Sprintf("%02X%02X%02X%02X/", sum[0], sum[1], sum[2], sum[3]))
		pos := strings.IndexByte(curhost, byte('.'))
		if pos < 0 {
			break
		}
		curhost = curhost[pos+1:]
	}
	return hashparam.String(), hashes
}

func (d *Dnsfilter) checkSafeBrowsing(host string) (Result, error) {
	format := func(hashparam string) string {
		url := fmt.Sprintf(defaultSafebrowsingURL, d.config.safeBrowsingServer, hashparam)
		return url
	}
	handleBody := func(body []byte, hashes map[string]bool) (Result, error) {
		result := Result{}
		scanner := bufio.NewScanner(strings.NewReader(string(body)))
		for scanner.Scan() {
			line := scanner.Text()
			splitted := strings.Split(line, ":")
			if len(splitted) < 3 {
				continue
			}
			hash := splitted[2]
			if _, ok := hashes[hash]; ok {
				// it's in the hash
				result.IsFiltered = true
				result.Reason = FilteredSafeBrowsing
				result.Rule = splitted[0]
				break
			}
		}

		if err := scanner.Err(); err != nil {
			// error, don't save cache
			return Result{}, err
		}
		return result, nil
	}
	result, err := d.lookupCommon(host, &stats.Safebrowsing, safebrowsingCache, true, format, handleBody)
	return result, err
}

func (d *Dnsfilter) checkParental(host string) (Result, error) {
	format2 := func(hashparam string) string {
		url := fmt.Sprintf(defaultParentalURL, hashparam, d.config.parentalSensitivity)
		return url
	}
	handleBody2 := func(body []byte, hashes map[string]bool) (Result, error) {
		// parse json
		var m []struct {
			Blocked   bool   `json:"blocked"`
			ClientTTL int    `json:"clientTtl"`
			Reason    string `json:"reason"`
		}
		err := json.Unmarshal(body, &m)
		if err != nil {
			// error, don't save cache
			log.Printf("Couldn't parse json '%s': %s", body, err)
			return Result{}, err
		}

		result := Result{}

		for i := range m {
			if m[i].Blocked {
				result.IsFiltered = true
				result.Reason = FilteredParental
				result.Rule = fmt.Sprintf("parental %s", m[i].Reason)
				break
			}
		}
		return result, nil
	}
	result, err := d.lookupCommon(host, &stats.Parental, parentalCache, false, format2, handleBody2)
	return result, err
}

// real implementation of lookup/check
func (d *Dnsfilter) lookupCommon(host string, lookupstats *LookupStats, cache gcache.Cache, hashparamNeedSlash bool, format func(hashparam string) string, handleBody func(body []byte, hashes map[string]bool) (Result, error)) (Result, error) {
	// if host ends with a dot, trim it
	host = strings.ToLower(strings.Trim(host, "."))

	// check cache
	cachedValue, isFound, err := getCachedReason(cache, host)
	if isFound {
		atomic.AddUint64(&stats.Safebrowsing.CacheHits, 1)
		return cachedValue, nil
	}
	if err != nil {
		return Result{}, err
	}

	// convert hostname to hash parameters
	hashparam, hashes := hostnameToHashParam(host, hashparamNeedSlash)

	// format URL with our hashes
	url := format(hashparam)

	// do HTTP request
	atomic.AddUint64(&lookupstats.Requests, 1)
	atomic.AddInt64(&lookupstats.Pending, 1)
	updateMax(&lookupstats.Pending, &lookupstats.PendingMax)
	resp, err := d.client.Get(url)
	atomic.AddInt64(&lookupstats.Pending, -1)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		// error, don't save cache
		return Result{}, err
	}

	// get body text
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		// error, don't save cache
		return Result{}, err
	}

	// handle status code
	switch {
	case resp.StatusCode == 204:
		// empty result, save cache
		cache.Set(host, Result{})
		return Result{}, nil
	case resp.StatusCode != 200:
		// error, don't save cache
		return Result{}, nil
	}

	result, err := handleBody(body, hashes)
	if err != nil {
		// error, don't save cache
		return Result{}, err
	}

	cache.Set(host, result)
	return result, nil
}

//
// Adding rule and matching against the rules
//

// AddRule adds a rule, checking if it is a valid rule first and if it wasn't added already
func (d *Dnsfilter) AddRule(input string, filterListID uint32) error {
	input = strings.TrimSpace(input)
	d.storageMutex.RLock()
	_, exists := d.storage[input]
	d.storageMutex.RUnlock()
	if exists {
		// already added
		return ErrInvalidSyntax
	}

	if !isValidRule(input) {
		return ErrInvalidSyntax
	}

	rule := Rule{
		text:         input, // will be modified
		originalText: input,
		listID:       filterListID,
	}

	// mark rule as whitelist if it starts with @@
	if strings.HasPrefix(rule.text, "@@") {
		rule.isWhitelist = true
		rule.text = rule.text[2:]
	}

	err := rule.parseOptions()
	if err != nil {
		return err
	}

	rule.extractShortcut()

	if !enableDelayedCompilation {
		err := rule.compile()
		if err != nil {
			return err
		}
	}

	destination := d.blackList
	if rule.isImportant {
		destination = d.important
	} else if rule.isWhitelist {
		destination = d.whiteList
	}

	d.storageMutex.Lock()
	d.storage[input] = &rule
	d.storageMutex.Unlock()
	destination.Add(&rule)
	return nil
}

// matchHost is a low-level way to check only if hostname is filtered by rules, skipping expensive safebrowsing and parental lookups
func (d *Dnsfilter) matchHost(host string) (Result, error) {
	lists := []*rulesTable{
		d.important,
		d.whiteList,
		d.blackList,
	}

	for _, table := range lists {
		res, err := table.matchByHost(host)
		if err != nil {
			return res, err
		}
		if res.Reason.Matched() {
			return res, nil
		}
	}
	return Result{}, nil
}

//
// lifecycle helper functions
//

func New() *Dnsfilter {
	d := new(Dnsfilter)

	d.storage = make(map[string]*Rule)
	d.important = newRulesTable()
	d.whiteList = newRulesTable()
	d.blackList = newRulesTable()

	// Customize the Transport to have larger connection pool
	defaultRoundTripper := http.DefaultTransport
	defaultTransportPointer, ok := defaultRoundTripper.(*http.Transport)
	if !ok {
		panic(fmt.Sprintf("defaultRoundTripper not an *http.Transport"))
	}
	d.transport = defaultTransportPointer                           // dereference it to get a copy of the struct that the pointer points to
	d.transport.MaxIdleConns = defaultHTTPMaxIdleConnections        // default 100
	d.transport.MaxIdleConnsPerHost = defaultHTTPMaxIdleConnections // default 2
	d.client = http.Client{
		Transport: d.transport,
		Timeout:   defaultHTTPTimeout,
	}
	d.config.safeBrowsingServer = defaultSafebrowsingServer
	return d
}

func (d *Dnsfilter) Destroy() {
	d.transport.CloseIdleConnections()
}

//
// config manipulation helpers
//

func (d *Dnsfilter) EnableSafeBrowsing() {
	d.config.safeBrowsingEnabled = true
}

func (d *Dnsfilter) EnableParental(sensitivity int) error {
	switch sensitivity {
	case 3, 10, 13, 17:
		d.config.parentalSensitivity = sensitivity
		d.config.parentalEnabled = true
		return nil
	default:
		return ErrInvalidParental
	}
}

func (d *Dnsfilter) EnableSafeSearch() {
	d.config.safeSearchEnabled = true
}

func (d *Dnsfilter) SetSafeBrowsingServer(host string) {
	if len(host) == 0 {
		d.config.safeBrowsingServer = defaultSafebrowsingServer
	} else {
		d.config.safeBrowsingServer = host
	}
}

func (d *Dnsfilter) SetHTTPTimeout(t time.Duration) {
	d.client.Timeout = t
}

func (d *Dnsfilter) ResetHTTPTimeout() {
	d.client.Timeout = defaultHTTPTimeout
}

func (d *Dnsfilter) SafeSearchDomain(host string) (string, bool) {
	if d.config.safeSearchEnabled == false {
		return "", false
	}
	val, ok := safeSearchDomains[host]
	return val, ok
}

//
// stats
//

func (d *Dnsfilter) GetStats() Stats {
	return stats
}

func (d *Dnsfilter) Count() int {
	return len(d.storage)
}

//
// cache control, right now needed only for tests
//
func purgeCaches() {
	safebrowsingCache.Purge()
	parentalCache.Purge()
}
