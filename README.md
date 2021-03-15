# jiratime

Jiratime is a tool for calculating work time logged in the jira cloud platform

It was created when the author discovered there was no apparent way to create
a timesheet from the Jira cloud platform's web GUI.  It may or may not work
for you.

## Building

```bash
go build jiratime.go
```

## Usage

```
jiratime [ -config <filename>] [-user <username>] [-format <output_format>]
         [-start <YYY-MM-DD>] [-end <YYYY-MM-DD>]`
```

### Options
-config \<filename>  
    Configuration file to use.  By default this is jiratime.json, located in
    $HOME/Library/Application Support on MacOS, $XDG_CONFIG_HOME if defined
    or $HOME/.config otherwise on Linux

-user \<username>  
    Email address of the user to retrieve work times for.  Defaults to the
    user running the query

-format \<output_format>  
    How to display output.  Options are:
    * text : Simple text
    * json : JSON
    * indent : indented JSON
    * csv : Comma separated values

-start \<YYYY-MM-DD>    
    Only report time in worklogs started on or after this day in the user's
    Time Zone

-end \<YYYY-MM-DD>  
    Only report time in worklogs starting on or before this day in the user's
    Time Zone

## Configuration file
The configuration file should be a json-formatted file specifying:
* "baseurl"  : https://<your-domain>.atlassian.net
* "username" : email address of the user to perform the query as
* "userkey" : API key for the user to perform the query as.
* "workers" : (optional) Number of worker subprocesses analysing worklogs

All keys are compulsory except "workers" which defaults to 20 if not specified

For example:
```json
{
    "baseurl" : "https://example.atlassian.net",
    "username" : "alice@example.com",
    "userkey" : "ABCDefGHIjKlMn123456789Z",
    "workers" : 10
}
```

## Start and End times
Time logged by a user is included in the report if it started on or after the
beginning of the day specified by the "-start" option and on or before the end
of the day specified by the "-end" option.  Times are relative to the user's
TimeZone.  If neither start nor end dates are given, all work logged by a
user is included in the report. If start is specified but not end, all work
logs started on or after the start date are included.  If end is specified but
not start, all work logs started before the end of the end date are included.

Note that if the start of the worklog is between the start and end boudaries
*all* work for that entry is included in the report, even if it extends beyond
midnight on the report's end date.

## API keys
Instructions on obtaining an API key for use with this program may be found
on the Atlassian web site:
https://support.atlassian.com/atlassian-account/docs/manage-api-tokens-for-your-atlassian-account/

## Permissions
Users need permission to view worklogs of users specified by the "-user"
option if that option is to be used successfully.
