package oneCRL

import (
	"bufio"
	"bytes"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"github.com/mozilla/OneCRL-Tools/config"
	"github.com/mozilla/OneCRL-Tools/util"
)

const IssuerPrefix string = "issuer: "
const SerialPrefix string = "serial: "


// TODO: this looks unecessary - maybe remove
type OneCRLUpdate struct {
	Data Record `json:"data"`
}

type Record struct {
	IssuerName   string `json:"issuerName"`
	SerialNumber string `json:"serialNumber"`
	Subject      string `json:"subject,omitempty"`
	PubKeyHash   string `json:"pubKeyHash,omitempty"`
	Enabled      bool   `json:"enabled"`
	Details struct {
		Who     string `json:"who"`
		Created string `json:"created"`
		Bug     string `json:"bug"`
		Name    string `json:"name"`
		Why     string `json:"why"`
	} `json:"details"`
}

func (record Record) EqualsRecord(otherRecord Record) bool {
	return record.IssuerName == otherRecord.IssuerName &&
		record.SerialNumber == otherRecord.SerialNumber &&
		record.Subject == otherRecord.Subject &&
		record.PubKeyHash == otherRecord.PubKeyHash
}

type Records struct {
	Data []Record
}

func StringFromRecord(record Record) string {
	if "" != record.Subject {
		return stringFromSubjectPubKeyHash(record.Subject, record.PubKeyHash)
	}
	return StringFromIssuerSerial(record.IssuerName, record.SerialNumber)
}

func stringFromSubjectPubKeyHash(subject string, pubKeyHash string) string {
	return fmt.Sprintf("subject: %s pubKeyHash: %s", subject, pubKeyHash)
}

func StringFromIssuerSerial(issuer string, serial string) string {
	return fmt.Sprintf("issuer: %s serial: %s", issuer, serial)
}


func getDataFromURL(url string, user string, pass string) ([]byte, error) {

	req, err := http.NewRequest("GET", url, nil)
	if len(user) > 0 {
		req.SetBasicAuth(user, pass)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if nil != err {
		return nil, err
	}

	defer resp.Body.Close()

	return ioutil.ReadAll(resp.Body)
}

func FetchExistingRevocations(url string) ([]string, error) {
	conf := config.GetConfig()

	if len(url) == 0 {
		return nil, errors.New("No URL was specified")
	}

	if "yes" == conf.OneCRLVerbose {
		fmt.Printf("Got URL data\n")
	}

	var existing []string

	user, pass := conf.KintoUser, conf.KintoPassword

	res := new(Records)
	data, err := getDataFromURL(url, user, pass)
	if nil != err {
		return nil, errors.New(fmt.Sprintf("problem loading existing data from URL %s", err))
	}

	err = json.Unmarshal(data, res)
	if nil != err {
		return nil, err
	}

	existing = make([]string, len(res.Data))
	for idx := range res.Data {
		existing[idx] = StringFromRecord(res.Data[idx])
	}

	return existing, nil
}

func ByteArrayEquals(a []byte, b []byte) bool {
    if len(a) != len(b) {
        return false
    }
    for i, v := range a {
        if v != b[i] {
            return false
        }
    }
    return true
}

func DNToRFC4514(name string) (string, error) {
	rawDN, _ := base64.StdEncoding.DecodeString(name)
	rdns := new(pkix.RDNSequence)
	_, err := asn1.Unmarshal(rawDN, rdns)
	
	return RFC4514ish(*rdns), err
}

func hexify(arr []byte, separate bool, upperCase bool) string {
	var encoded bytes.Buffer
	for i := 0; i < len(arr); i++ {
		encoded.WriteString(strings.ToUpper(hex.EncodeToString(arr[i : i+1])))
		if i < len(arr)-1 && separate {
			encoded.WriteString(":")
		}
	}
	retval := encoded.String()
	if !upperCase {
		retval = strings.ToLower(retval)
	}
	return retval
}

func SerialToString(encoded string, separate bool, upper bool) (string, error) {
	rawSerial, err := base64.StdEncoding.DecodeString(encoded)
	return hexify(rawSerial, separate, upper), err
}

func NamesDataMatches(name1 []byte, name2 []byte) bool {
	// Go's asn.1 marshalling support does not maintain original encodings.
	// Because if this, if the data are the same other than the encodings then
	// although bytewise comparisons on the original data failed, we can assume
	// that encoding differences will go away when we marshal back from
	// pkix.RDNSequence back to actual asn.1 data.

	// ensure our names decode to pkix.RDNSequences
	rdns1 := new(pkix.RDNSequence)
	_, errUnmarshal1 := asn1.Unmarshal(name1, rdns1)
	if nil != errUnmarshal1 {
		return false
	}

	rdns2 := new(pkix.RDNSequence)
	_, errUnmarshal2 := asn1.Unmarshal(name2, rdns2)
	if nil != errUnmarshal2 {
		return false
	}

	marshalled1, marshall1err := asn1.Marshal(*rdns1)
	if nil != marshall1err {
		return false
	}
	marshalled2, marshall2err := asn1.Marshal(*rdns2)
	if nil != marshall2err {
		return false
	}

	return ByteArrayEquals(marshalled1, marshalled2)
}

func RFC4514ish(rdns pkix.RDNSequence) string {
	retval := ""
	for _, rdn := range rdns {
		if len(rdn) == 0 {
			continue
		}
		atv := rdn[0]
		value, ok := atv.Value.(string)
		if !ok {
			continue
		}
		t := atv.Type
		tStr := ""
		if len(t) == 4 && t[0] == 2 && t[1] == 5 && t[2] == 4 {
			switch t[3] {
			case 3:
				tStr = "CN"
			case 7:
				tStr = "L"
			case 8:
				tStr = "ST"
			case 10:
				tStr = "O"
			case 11:
				tStr = "OU"
			case 6:
				tStr = "C"
			case 9:
				tStr = "STREET"
			}
		}
		if len(t) == 7 &&
			t[0] == 1 &&
			t[1] == 2 &&
			t[2] == 840 &&
			t[3] == 113549 &&
			t[4] == 1 &&
			t[5] == 9 &&
			t[6] == 1 {
				tStr = "emailAddress"
			}

		sep := ""
		if len(retval) > 0 {
			sep = ", "
		}

		// quote values that contain a comma
		if strings.Contains(value, ",") {
			value = "\"\"" + value + "\"\""
		}
		retval = retval + sep + tStr + "=" + value
	}
	return retval
}

type OneCRLLoader interface {
	LoadRecord(record Record)
}

// TODO: fix loading functions to get data from a reader

func LoadJSONFromURL(url string, loader OneCRLLoader) error {
	var err error
	res := new(Records)
	r, err := http.Get(url)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	err = json.NewDecoder(r.Body).Decode(res)
	if nil != err {
		return err
	}

	for idx := range res.Data {
		loader.LoadRecord(res.Data[idx])
	}

	return nil
}

func LoadRevocationsTxtFromFile(filename string, loader OneCRLLoader) error {
	var (
		err error
	)
	file, err := os.Open(filename)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var dn = ""
	for scanner.Scan() {
		// process line
		line := scanner.Text()
		// Ignore comments
		if 0 == strings.Index(line, "#") {
			continue
		}
		if 0 == strings.Index(line, " ") {
			if len(dn) == 0 {
				log.Fatal("A serial number with no issuer is not valid. Exiting.")
			}
			record := Record{IssuerName:dn, SerialNumber:strings.Trim(line," ")}
			loader.LoadRecord(record)
			continue
		}
		if 0 == strings.Index(line, "\t") {
			log.Fatal("revocations.txt containing subject / pubkey pairs not yet supported")
			log.Fatal("A public key hash with no subject is not valid. Exiting.")
		}
		dn = line
	}
	
	if err = scanner.Err(); err != nil {
		log.Fatal(err)
	}

	return nil
}

func LoadRevocationsFromBug(filename string, loader OneCRLLoader) error {
	conf := config.GetConfig()
	file, err := os.Open(filename)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		// process line
		line := scanner.Text()

		// parse the issuer and serial lines from the bug data
		issuerIndex := strings.Index(line, IssuerPrefix)
		serialIndex := strings.Index(line, SerialPrefix)

		issuer := line[issuerIndex + len(IssuerPrefix): serialIndex - 1]
		serial := line[serialIndex + len(SerialPrefix): len(line)]

		if "yes" == conf.OneCRLVerbose {
			fmt.Printf("Loading revocation. issuer: \"%s\", serial: \"%s\"\n", issuer, serial)
		}

		record := Record{IssuerName:issuer, SerialNumber:serial}
		loader.LoadRecord(record)
	}

	if err = scanner.Err(); err != nil {
		log.Fatal(err)
	}

	return nil
}

func AddEntries(records *Records, createBug bool) error {
	conf := config.GetConfig()

	issuerMap := make(map[string][]string)

	attachment := ""

	bugNum := -1

	shouldWrite := conf.Preview != "yes" && len(records.Data) > 0

	now := time.Now()
	nowString := now.Format("2006-01-02T15:04:05Z")

	if shouldWrite {
		bug := bugs.Bug{}
		bug.ApiKey = conf.BugzillaAPIKey
		blocks, err := strconv.Atoi(conf.BugzillaBlockee)
		if len(conf.BugzillaBlockee) != 0 {
			if nil == err {
				bug.Blocks = append(bug.Blocks, blocks)
			}
		}
		bug.Product = "Toolkit"
		bug.Component = "Blocklisting"
		bug.Version = "unspecified"
		bug.Summary = fmt.Sprintf("CCADB entries generated %s", nowString)
		bug.Description = conf.BugDescription

		bugNum, err = bugs.CreateBug(bug, conf)
		if err != nil {
			panic(err)
		}
	}

	for _, record := range records.Data {
		// TODO: We don't need to build an issuer map if we're not outputting
		// entries directly. If we *do* need to do this, the functionality for
		// making revocations.txt style data should live in oneCRL.go
		if issuers, ok := issuerMap[record.IssuerName]; ok {
			issuerMap[record.IssuerName] = append(issuers, record.SerialNumber)
		} else {
			issuerMap[record.IssuerName] = []string{record.SerialNumber}
		}

		if record.Details.Bug == "" {
			record.Details.Bug = fmt.Sprintf("%s/show_bug.cgi?id=%d",conf.BugzillaBase, bugNum)
		}
		if record.Details.Created == "" {
			record.Details.Created = nowString
		}
		
		update := new(OneCRLUpdate)
		update.Data = record
		marshalled, _ := json.Marshal(update)

		// Upload the created entry to Kinto
		// TODO: Batch these, don't send single requests
		if conf.Preview != "yes" {
			if "yes" == conf.OneCRLVerbose {
				fmt.Printf("Will POST to \"%s\" with \"%s\"\n", conf.KintoCollectionURL + "/records", marshalled)
			}
			req, err := http.NewRequest("POST", conf.KintoCollectionURL + "/records", bytes.NewBuffer(marshalled))

			if len(conf.KintoUser) > 0 {
				req.SetBasicAuth(conf.KintoUser, conf.KintoPassword)
			}
			req.Header.Set("Content-Type", "application/json")

			client := &http.Client{}
			resp, err := client.Do(req)

			if nil != err {
				panic(err)
			}

			if "yes" == conf.OneCRLVerbose {
				fmt.Printf("status code is %d\n", resp.StatusCode)
				fmt.Printf("record data is %s\n", StringFromRecord(record))
			}
			attachment = attachment + StringFromRecord(record) + "\n"
			defer resp.Body.Close()

			if err != nil {
				panic(err)
			}
		} else {
			fmt.Printf("Would POST to \"%s\" with \"%s\"\n", conf.KintoCollectionURL + "/records", marshalled)
		}
	}

	// TODO: request review on the Kinto change
	if shouldWrite {
		// TODO: Factor out the request stuff...
		reviewJSON := "{\"data\": {\"status\": \"to-review\"}}"

		// PATCH the object to set the status to to-review
		req, err := http.NewRequest("PATCH", conf.KintoCollectionURL, bytes.NewBuffer([]byte(reviewJSON)))

		if len(conf.KintoUser) > 0 {
			req.SetBasicAuth(conf.KintoUser, conf.KintoPassword)
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)

		if "yes" == conf.OneCRLVerbose {
			fmt.Printf("requested review - status code is %d\n", resp.StatusCode)
		}

		defer resp.Body.Close()

		if err != nil {
			panic(err)
		}


		// upload the created entries to bugzilla
		attachments := make([]bugs.Attachment, 1)
		data := []byte(attachment)
		str := base64.StdEncoding.EncodeToString(data)
		attachments[0] = bugs.Attachment{}
		attachments[0].ApiKey = conf.BugzillaAPIKey
		attachments[0].Data = str


		attachments[0].Flags = make([]bugs.AttachmentFlag,0,1)
		// create flags for the reviewers
		for _, reviewer := range strings.Split(conf.BugzillaReviewers, ",") {
			trimmedReviewer := strings.Trim(reviewer," ")
			if len(trimmedReviewer) > 0 {
				flag := bugs.AttachmentFlag{}
				flag.Name = "review"
				flag.Status = "?"
				flag.Requestee = trimmedReviewer
				flag.New = true
				attachments[0].Flags = append(attachments[0].Flags, flag)
			}
		}

		err = bugs.AttachToBug(bugNum, conf.BugzillaAPIKey, attachments, conf)
		if err != nil {
			fmt.Printf(str)
			panic(err)
		}
	}

	// TODO: put output into the bug
	return nil
}
