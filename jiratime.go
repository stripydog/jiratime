// Copyright Keith Young 2021
// For copying information, see the file COPYING distributed with this file
//
// jiratime sums jira worklog entries for a user specified by their email
// address which begin between a specified start and end date 
package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "log"
    "net/http"
    "net/url"
    "os"
    "strconv"
    "sync"
    "time"
)

// Output formats
const (
    fmt_text = iota
    fmt_json
    fmt_indent
    fmt_csv
)

// Number of worker goroutines (concurrently active worklog queries)
const defWorkers = 20

// Size of buffers for inter-go-routine communication
const idbuf = 50

// Time constants (in seconds.  Those in the time package are in nanoseconds)
const minute = 60
const hour = 60 * minute

type config struct {
    Baseurl string
    Username string
    Userkey string
    Workers int         // (optional in config) To override defWorkers
}

var conf config

// client to use
var httpc = &http.Client {
    Timeout : 10 * time.Second,
}

type userinfo struct {
    AccountID string `json:"accountId,omitempty"`
    EmailAddress string `json:"emailAddress"`
    DisplayName string `json:"displayName"`
    TimeZone string `json:"timeZone"`
}

type results struct {
    User *userinfo `json:"user"`
    Start string `json:"start,omitempty"`
    End string `json:"end,omitempty"`
    Hours uint `json:"hours"`
    Minutes uint `json:"minutes"`
    Seconds uint `json:"seconds"`
    TotalSeconds uint `json:"totalSeconds"`
    Format  int `json:"-"`
}
// displayResults does what the name suggests.  Future versions of this
// Program will offer alternative output formats
func displayResults(res *results) {

    switch res.Format {
    case fmt_text:
        fmt.Printf("%-25s%8s%8s\n%10s - %10s: %8d%8d\n",
                res.User.DisplayName, "Hours","Minutes",
                res.Start,res.End,res.Hours,res.Minutes)
    case fmt_json:
        if j, err := json.Marshal(res); err == nil {
            fmt.Println(string(j))
        }
    case fmt_indent:
        if j, err := json.MarshalIndent(res,"","    "); err == nil {
            fmt.Println(string(j))
        }
    case fmt_csv:
        fmt.Printf("%s,%s,%s,%s,%s,%d,%d,%d\n",res.User.DisplayName,
                res.User.EmailAddress,res.Start,res.End,res.User.TimeZone,
                res.Hours,res.Minutes,res.TotalSeconds)
    default:
        log.Fatal(fmt.Errorf("Unrecognised display format requested"))
    }
}

// getConfig loads configuration from a json file
func getConfig(c *config, filename *string) error {

    var undef string

    file, err := os.Open(*filename)
    if err != nil {
        return err
    }

    defer file.Close()
    if err = json.NewDecoder(file).Decode(c); err != nil {
        return err
    }

    // Check required values present in config
    if c.Baseurl == "" {
        undef = "baseurl,"
    }
    if c.Username == "" {
        undef += "username,"
    }
    if c.Userkey == "" {
        undef += "userkey,"
    }

    if len(undef) == 0 {
        c.Baseurl += "/rest/api/3"
        return nil
    }

    // Remove pesky trailing "," from list of undefined values
    return fmt.Errorf("Not defined in config: %s",undef[:len(undef)-1])
}

// getCaller retrievs information on the account used to authenticate to
// the jira APIs
func getCaller() (*userinfo, error) {
    
    req, err := http.NewRequest("GET", conf.Baseurl + "/myself", nil)
    if err != nil {
        return nil,err
    }

    req.Header.Add("Accept", "application/json")
    req.SetBasicAuth(conf.Username,conf.Userkey)

    resp,err := httpc.Do(req)
    if  err != nil {
        return nil,err
    }

    defer resp.Body.Close()

    caller := &userinfo{}

    err = json.NewDecoder(resp.Body).Decode(caller)


    if caller.AccountID == "" {
        err = fmt.Errorf("Could not determine AccountID for %s", conf.Username)
    }

    return caller,err
}

// getUser retrieves the account ID for a given user's email address
func getUser(user string) (*userinfo, error) {
    req, err := http.NewRequest("GET", conf.Baseurl + "/user/search", nil)
    if err != nil {
        return nil,err
    }

    p := url.Values{}
    p.Set("query",user)

    req.URL.RawQuery = p.Encode()

    req.Header.Add("Accept", "application/json")

    req.SetBasicAuth(conf.Username,conf.Userkey)

    resp,err := httpc.Do(req)
    if  err != nil {
        return nil,err
    } 

    defer resp.Body.Close()

    arr := []userinfo{}

    err = json.NewDecoder(resp.Body).Decode(&arr)
    if err != nil {
        return nil,err
    }

    if len(arr) == 0 || arr[0].AccountID == "" {
        return nil,fmt.Errorf("Could not determine AccountID for %s", user)
    }

    u := arr[0]
    if u.EmailAddress == "" {
        u.EmailAddress = user
    }
    return &u, nil
}

// getIDs() retrieves the IDs of all issues which are assigned to the current
// user and writes them to a channel for consumption by worker goroutines
func getIDs(user string,start, end time.Time,ids chan<- string) {

    defer  close(ids)

    req, err := http.NewRequest("GET", conf.Baseurl + "/search", nil)
    if err != nil {
        log.Fatalf("%s\n",err)
    }

    // Note that apparently contrary to the API documentation, worklogs
    // are not returned with jql queries, otherwise we could process them
    // without requiring further lookups
    j := struct {
        MaxResults uint
        Total uint
        StartAt uint
        Issues [] struct {
            Id string
        }
    }{}

    p := url.Values{}

    // Look for issues where our user has made a worklog entry there
    // are worklog entries between the start and end dates.  Note that
    // worklogDate doesn't behave as the API documentation claimes (and
    // there is a long-open bug about this).  Putting a time specifier
    // into the time you are comparing worklogDate against doesn't work
    // Hence we need to use a slightly bigger range to search and hence
    // <= rather than < when comparing to end
    jql := "worklogAuthor = " + user
    if !start.IsZero() {
        jql += " AND worklogDate >= \"" + start.Format("2006-01-02") + "\""
    }
    if !end.IsZero() {
        jql += " AND worklogDate <= \"" + end.Format("2006-01-02") + "\""
    }
    p.Set("jql",jql)
    p.Set("fields","id")
    p.Set("maxResults","100")       // Max this can be set to in v3 api

    req.URL.RawQuery = p.Encode()

    req.Header.Add("Accept", "application/json")

    req.SetBasicAuth(conf.Username,conf.Userkey)

    // Only maxResults can be retrieved with each query so if total
    // results > maxResults, need to loop, incrementing startAt each time
    for {
        resp,err := httpc.Do(req)
        if  err != nil {
            log.Fatalf("Failed to obtain issue list: %v", err)
        }

        err = json.NewDecoder(resp.Body).Decode(&j)

        if err != nil {
            log.Fatalf("Failed to decode response body: %v\n",err)
        }

        resp.Body.Close()

        for _, v := range j.Issues {
            ids <- v.Id
        }

        // End loop if we've retrieved all the results
        if j.Total - j.StartAt < j.MaxResults {
            break
        }

        // Incrememnt startAt before re-querying
        p.Set("startAt",strconv.FormatUint(uint64(j.StartAt + j.MaxResults),10))
        req.URL.RawQuery = p.Encode()
    }

}

// getWork() reads issue ID strings from the "ids"  channel, queries for
// the issue's worklogs, sums the time logged for worklogs which begin
// between the dates in the start and end time parameters and writes the
// results to the workTime channel
func getWork(user string,start,end time.Time,ids <-chan string,
        workTime chan<- uint, wg *sync.WaitGroup) {

    // Loop until ids channel is closed (no more issues)
    for issue := range ids {
        totalTime := uint(0)

        req, err := http.NewRequest("GET", conf.Baseurl + "/issue/" + issue +
                "/worklog", nil)
        if err != nil {
            log.Fatal(err)
        }

        j := struct {
            Worklogs []struct {
                Author struct {
                    AccountID string
                }
                Started string
                TimeSpentSeconds uint
            }
            StartAt uint
            Total uint
            MaxResults uint
        }{}

        p := url.Values{}
        p.Set("fields","worklogs")
        p.Set("maxResults","100")

        req.URL.RawQuery = p.Encode()

        req.Header.Add("Accept", "application/json")

        req.SetBasicAuth(conf.Username,conf.Userkey)

        // Retrieve (the api's idea of, not our suggestion) maxResults
        // results at a time until we've processed all the worklogs
        for {
            resp,err := httpc.Do(req)
            if  err != nil {
                log.Fatal(err)
            }
            err = json.NewDecoder(resp.Body).Decode(&j)

            if err != nil {
                log.Fatal(err)
            }

            for _, w := range j.Worklogs {
                started, err := time.Parse("2006-01-02T15:04:05-0700",w.Started)
                if  err != nil {
                    log.Printf("jiratime: failed to parse Worklog start: %v\n",
                            err)
                    continue
                }
                if w.Author.AccountID != user {
                    continue
                }
                if !(start.IsZero() || !started.Before(start)) {
                    continue
                }
                if end.IsZero() || started.Before(end) {
                    totalTime += w.TimeSpentSeconds
                }
            }

            // If we're done for this issue, write totalTime to the workTime
            // channel and continue with the next issue
            if j.Total - j.StartAt < j.MaxResults {
                if totalTime != 0 {
                    workTime<- totalTime
                }
                break
            }

            // If we're not done, retrieve the next j.MaxResults worklogs
            p.Set("startAt",strconv.FormatUint(uint64(j.StartAt + j.MaxResults),
                    10))
            req.URL.RawQuery = p.Encode()
        }
    }
    wg.Done()
}

func main() {

    var parsefail int
    var start, end time.Time

    res := &results {}

    // Make logging less verbose
    log.SetFlags(log.Lshortfile)

    confDir, err := os.UserConfigDir()
    if err != nil {
        log.Fatal(err)
    }
    
    // Argument processing
    startp := flag.String("start","","First day of report (YYYY-MM-DD)")
    endp := flag.String("end","","Last day of report (YYYY-MM-DD)")
    confp := flag.String("config",confDir + "/jiratime.json",
            "Configuration file")
    userp := flag.String("user","","User's email address")
    fmtp := flag.String("format","text","Output format")

    flag.Parse()

    // Determine output format
    switch *fmtp {
    case "text":
        res.Format = fmt_text
    case "json":
        res.Format = fmt_json
    case "indent":
        res.Format = fmt_indent
    case "csv":
        res.Format = fmt_csv
    default:
        log.Fatalf("Unknown output format: %s\n",*fmtp)
    }

    if err = getConfig(&conf,confp); err != nil {
        log.Fatalf("Failed to load config: %v\n",err)
    }

    // Determine info about caller: Needed for TimeZone as serves as
    // default username for query
    caller, err := getCaller()
    if err != nil {
        log.Fatal(err)
    }

    callerLoc,err := time.LoadLocation(caller.TimeZone)
    if err != nil {
        log.Fatal(err)
    }

    // Who are we running the query for?  Defaults to user running the query
    var user *userinfo
    var userLoc *time.Location

    if *userp == "" {
        user = caller
        userLoc = callerLoc
    } else {
        user, err = getUser(*userp)
        if err != nil {
            log.Fatal(err)
        }

        userLoc, err = time.LoadLocation(user.TimeZone)
        if err != nil {
            log.Fatal(err)
        }
    }

    res.User = user

    // Parse end date even if start date parsing fails so we can flag
    // errors if both are wrong
    if *startp != "" {
        start, err = time.ParseInLocation("2006-01-02",*startp,userLoc)
        if err != nil {
            log.Printf("jiratime: failed to parse start date: %v\n",err)
            parsefail++
        }
    }
    if *endp != "" {
        end, err = time.ParseInLocation("2006-01-02",*endp,userLoc)
        if  err != nil {
            log.Printf("jiratime: failed to parse end date: %v\n",err)
            parsefail++
        }

        // "end" is "inclusive" so make it the end of the day
        // time package does account for DST (leap seconds not tested...)
        end = end.AddDate(0,0,1).Add(-1)
    }

    if (parsefail > 0) {
        os.Exit(1)
    }

    // Jira APIs use caller's timezone, so convert if user we are querying
    // for is not the user we're calling as
    if *userp != "" {
        if !start.IsZero() {
            start = start.In(callerLoc)
        }
        if !end.IsZero() {
            end = end.In(callerLoc)
        }
    }

    if conf.Workers == 0 {
        conf.Workers = defWorkers
    }

    // Channel for passing issues to workers
    ids := make(chan string, idbuf)

    // Channel for passing time spent from workers to main
    workTime := make(chan uint, conf.Workers)

    // Wait Group used to wait for workers to terminate before program exit
    var wg sync.WaitGroup

    // Obtain a list of issue IDs where user is the assignee and update time
    // is after the specified start time

    go getIDs(user.AccountID,start,end,ids)

    // Start workers
    for i := 0; i < conf.Workers; i++ {
        wg.Add(1)
        go getWork(user.AccountID,start,end,ids, workTime, &wg)
    }

    // Wait for all works to terminate before closing the workTime channel
    // which causes results to be printed and program to terminate
    go func() {
        wg.Wait()
        close(workTime)
    }()

    // Accumulate issue time totals until workTime channel closed following
    // termination of last worker thread
    for logged := range workTime {
        res.TotalSeconds += logged
    }

    // Add start and end dates to results, formatted as strings
    if !start.IsZero() {
        res.Start = start.Format("2006-01-02")
    }
    if !end.IsZero() {
        res.End = end.Format("2006-01-02")
    }

    // Split into Hours/Mins/Secs
    timeSpentSeconds := res.TotalSeconds
    res.Hours = timeSpentSeconds / hour
    timeSpentSeconds %= hour
    res.Minutes = timeSpentSeconds / minute
    res.Seconds = timeSpentSeconds % minute
    res.User.AccountID = ""
    displayResults(res)
}
