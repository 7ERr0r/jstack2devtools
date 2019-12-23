package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/process"
)

var verboseFlag = flag.Bool("v", false, "verbose output")
var ignoredPackagesFlag = flag.String("ignore", "org.lwjgl", "ignored packages separated by ,")
var ignoredPackages []string

var globalEvents = make([]interface{}, 0, 1024)
var profileEvents = make([]interface{}, 0, 1024)

var timeStart = 0
var samplerTimeUnit = int64(time.Microsecond)

type CallFrame struct {
	FunctionName string `json:"functionName"`
	URL          string `json:"url,omitempty"`
	ScriptID     int    `json:"scriptId"`
	LineNumber   int    `json:"lineNumber,omitempty"`
	ColumnNumber int    `json:"columnNumber,omitempty"`
}
type ProfileNode struct {
	Frame  *CallFrame `json:"callFrame"`
	ID     int        `json:"id"`
	Parent int        `json:"parent"`
}

//var callFrames = make(map[string]*CallFrame)
var profileNodes = make(map[string]*ProfileNode)
var profilesNodeByID = make([]*ProfileNode, 0, 128)
var profilesNodeIDcounter int
var newNodes = make([]*ProfileNode, 0, 128)

func init() {
	profilesNodeByID = append(profilesNodeByID, nil)
}
func addNode(key string, node *ProfileNode) {
	profileNodes[key] = node
	profilesNodeByID = append(profilesNodeByID, node)
	newNodes = append(newNodes, node)
}
func stripFuncName(stackElement string) (name, file, line string) {
	//stackElement = strings.ReplaceAll(stackElement, ".", "_")
	i := strings.LastIndex(stackElement, "(")
	if i >= 0 {
		name = stackElement[:i]
		fileLine := stackElement[i+1:]
		lineIndex := strings.Index(fileLine, ":")
		if lineIndex >= 0 {
			file = fileLine[:lineIndex]
			// todo checks
			line = fileLine[1+lineIndex : len(fileLine)-1]
		}
		return
	}
	name = stackElement
	return
}
func getProfileNode(frame []string) (node *ProfileNode) {
	key := strings.Join(frame, "|")
	node, contains := profileNodes[key]
	if contains {
		return
	}
	if len(frame) == 0 {
		node = new(ProfileNode)

		node.Frame = new(CallFrame)
		node.Frame.FunctionName = "(root)"
	} else {
		node = new(ProfileNode)

		node.Frame = new(CallFrame)
		name, file, line := stripFuncName(frame[0])
		node.Frame.FunctionName = name
		node.Frame.ScriptID = 1
		node.Frame.LineNumber, _ = strconv.Atoi(line)
		node.Frame.ColumnNumber = 1
		node.Frame.URL = "file://" + file
		node.Parent = getProfileNode(frame[1:]).ID
	}
	addNode(key, node)
	node.ID = len(profilesNodeByID) - 1
	return
}

func main() {
	isWindows := runtime.GOOS == "windows"
	exp := `.*javaw.*-cp.*`
	if isWindows {
		exp = `.*javaw.exe.*-cp.*`
	}
	stacksPath := flag.String("I", "stacks.txt", "path to stacks txt file")
	appCmdLineRegexp := flag.String("cmdline", exp, "regexp used to find profiled app by command line string")
	threadNameRegexp := flag.String("thread", "^Client thread$", "regexp used to find profiled thread by name")
	processNameContains := flag.String("nameContains", `java`, `executable name hint to speed up the search process, empty string "" to check all`)
	outputFilePath := flag.String("O", "events.json", "path to output json file")
	samples := flag.Int("samples", 100000, "number of samples collected")
	findOnly := flag.Bool("findOnly", false, "true to only find process ID and exit")
	flag.Parse()

	ignoredPackages = strings.Split(*ignoredPackagesFlag, ",")

	err := run(*stacksPath, *appCmdLineRegexp, *threadNameRegexp, *processNameContains, *outputFilePath, *samples, *findOnly)
	if err != nil {
		log.Printf("run error: %s", err)
	}

	time.Sleep(1 * time.Second)

}
func run(stacksPath, appCommandLineRegexp, threadNameRegexp, processNameContains, outputFilePath string, samples int, findOnly bool) (err error) {
	compiledCommandLineRegexp, err := regexp.Compile(appCommandLineRegexp)
	if err != nil {
		return
	}
	compiledThreadNameRegexp, err := regexp.Compile(threadNameRegexp)
	if err != nil {
		return
	}
	if findOnly {
		var foundCmd string
		var pid int
		foundCmd, pid, err = findPidByRegexp(compiledCommandLineRegexp, processNameContains)
		if err != nil {
			return
		}
		log.Printf("found: %s", foundCmd)
		log.Printf("pid: %d", pid)
		return

	}
	f, err := os.Open(stacksPath)
	if err != nil {
		return
	}
	br := bufio.NewReader(f)

	appendStartEvent()
	appendThreadEvent()
	appendProfileEvent(lastTime)

	times := make([]int, 0, samples)
	topNodes := make([]*ProfileNode, 0, samples)
	var topNode *ProfileNode
	//var bottomNode *ProfileNode
	idleNode := getProfileNode([]string{"lag(sampler_lag:1337)"})

	times = append(times, 0)
	//times = append(times, 1000000)
	//topNodes = append(topNodes, idleNode)

	delta := 0
	for i := 0; i < samples; i++ {
		log.Printf("sample: %d, delta: %dns", i, delta)

		delta, topNode, _, err = readStack(br, compiledThreadNameRegexp)
		if err != nil {
			if err == io.EOF {
				break
			}
			return
		}
		nowTime += int64(delta)

		constIdle := 2500000
		constBetween := 0
		idle := constIdle
		between := int(nowTime-lastTime) - idle

		if between <= constBetween {
			idle = constIdle - (between - constBetween)
			if idle <= 0 {
				idle = 0
			}
			between = constBetween
		}
		if between > 0 {
			topNodes = append(topNodes, idleNode)
			times = append(times, between) // 2ms is (idle) between samples
		}

		topNodes = append(topNodes, topNode)
		times = append(times, idle)

		lastTime = nowTime

	}
	times = times[:len(times)-1]

	nowTime += int64(delta)
	appendFunctionCallBeginEvent(topNode, 0)
	appendFunctionCallEndEvent(nowTime)

	nodesCopy := make([]*ProfileNode, len(newNodes))
	copy(nodesCopy, newNodes)
	newNodes = newNodes[:0]

	appendProfileChunkEvent(topNodes, times, nodesCopy, 0)

	globalEvents = append(globalEvents, profileEvents...)

	err = dumpCollectedEvents(outputFilePath)

	return
}

func dumpCollectedEvents(outputFilePath string) (err error) {
	f, err := os.Create(outputFilePath)
	if err != nil {
		return
	}
	enc := json.NewEncoder(f)
	enc.Encode(globalEvents)
	f.Close()

	return
}

var atBytes = []byte("\tat ")
var nameEnd = []byte(`"`)

var nowTime = int64(0)
var lastTime = int64(0)

func readStack(br *bufio.Reader, exp *regexp.Regexp) (delta int, topNode, bottomNode *ProfileNode, err error) {
	var line []byte
	var currentThread bool

	var stack = make([]string, 0, 8)
	for {

		line, _, err = br.ReadLine()
		if err != nil {
			if err == io.EOF {
				//err = nil
			}
			break
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		//log.Printf("reading line %s", line)

		if len(line) > 1 {
			if line[0] == '"' {
				currentThread = false

				nameEndIndex := bytes.Index(line[1:], nameEnd)
				if nameEndIndex > 0 {
					name := line[1 : 1+nameEndIndex]
					matches := exp.Match(name)
					if *verboseFlag {
						log.Printf("name: %s %v", name, matches)
					}
					if matches {
						currentThread = true
					}
				}
			} else if line[0] == '\t' {
				if currentThread {
					if bytes.HasPrefix(line, atBytes) {
						element := line[len(atBytes):]
						if *verboseFlag {
							log.Printf("element: %s", element)
						}
						stack = append(stack, string(element))
					} else {
						log.Printf("unrecognized line: %s", line)
					}
				}
			} else {
				if bytes.HasSuffix(line, []byte("ns")) {
					num := string(line[:len(line)-2])
					delta, err = strconv.Atoi(num)
					break
				}
			}
		}
	}
	containsIgnored := false
ignorance:
	for _, s := range stack {
		for _, ignoredStr := range ignoredPackages {
			if strings.Contains(s, ignoredStr) {
				containsIgnored = true
				break ignorance
			}
		}
	}
	if containsIgnored {
		stack = []string{"ignored"}
	}
	topNode = getProfileNode(stack)
	if len(stack) >= 1 {
		bottomNode = getProfileNode(stack[len(stack)-1:])
	}
	return
}

func findPidByRegexp(cmdlineExp *regexp.Regexp, processNameContains string) (foundCmd string, pid int, err error) {
	processes, err := process.Processes()
	if err != nil {
		return
	}
	for _, process := range processes {
		var name string
		name, err = process.Name()
		if err != nil {
			continue
		}
		if name != "" {
			if !strings.Contains(name, processNameContains) {
				continue
			}
		}
		var cmdlineS []string
		cmdlineS, err = process.CmdlineSlice()
		if err != nil {
			continue
		}
		for i := 0; i < len(cmdlineS); i++ {
			cmdlineS[i] = strconv.Quote(cmdlineS[i])
		}
		cmdline := strings.Join(cmdlineS, " ")
		if *verboseFlag {
			log.Printf("cmdline: %s", cmdline)
		}

		if cmdlineExp.Match([]byte(cmdline)) {
			pid = int(process.Pid)
			foundCmd = cmdline
			return
		}
	}
	err = fmt.Errorf("couldn't find process pid")
	return
}

const commonTimeAdded = 0

func appendStartEvent() {
	event := map[string]interface{}{
		"pid":  31337,
		"tid":  11504,
		"ts":   commonTimeAdded / samplerTimeUnit,
		"ph":   "I",
		"cat":  "disabled-by-default-devtools.timeline",
		"name": "TracingStartedInBrowser",
		"s":    "t",
		"tts":  0,
		"args": map[string]interface{}{
			"data": map[string]interface{}{
				"frameTreeNodeId": 1,
				"persistentIds":   true,
				"frames": []interface{}{
					map[string]interface{}{
						"frame":     "52287E0FF83D2BDA557C4C539D7A1E2A",
						"url":       "http://java",
						"name":      "",
						"processId": 1337,
					},
				},
			},
		},
	}
	globalEvents = append(globalEvents, event)
}
func appendThreadEvent() {
	event := map[string]interface{}{
		"pid":  31337,
		"tid":  11504,
		"ts":   0,
		"ph":   "M",
		"cat":  "__metadata",
		"name": "thread_name",
		"args": map[string]interface{}{
			"name": "CrBrowserMain",
		},
	}
	globalEvents = append(globalEvents, event)
}
func appendProfileEvent(t int64) {
	event := map[string]interface{}{
		"pid":  1337,
		"tid":  11504,
		"ts":   (commonTimeAdded + t) / samplerTimeUnit,
		"ph":   "P",
		"cat":  "disabled-by-default-v8.cpu_profiler",
		"name": "Profile",
		"id":   "0x4",
		"tts":  t / samplerTimeUnit,
		"args": map[string]interface{}{
			"data": map[string]interface{}{
				"startTime": 0,
			},
		},
	}
	profileEvents = append(profileEvents, event)
}
func appendFunctionCallBeginEvent(node *ProfileNode, t int64) {
	event := map[string]interface{}{
		"pid":  1337,
		"tid":  11504,
		"ts":   (commonTimeAdded + t) / samplerTimeUnit,
		"ph":   "B",
		"cat":  "devtools.timeline",
		"name": "FunctionCall",
		"tts":  t / samplerTimeUnit,
		"args": map[string]interface{}{
			"data": map[string]interface{}{
				"frame":        "52287E0FF83D2BDA557C4C539D7A1E2A",
				"functionName": node.Frame.FunctionName,
				"scriptId":     strconv.Itoa(node.Frame.ScriptID),
				"url":          node.Frame.URL,
				"lineNumber":   node.Frame.LineNumber,
				"columnNumber": node.Frame.ColumnNumber,
			},
		},
	}
	globalEvents = append(globalEvents, event)
}

func appendFunctionCallEndEvent(t int64) {
	event := map[string]interface{}{
		"pid":  1337,
		"tid":  11504,
		"ts":   (commonTimeAdded + t) / samplerTimeUnit,
		"ph":   "E",
		"cat":  "devtools.timeline",
		"name": "FunctionCall",
		"tts":  t / samplerTimeUnit,
		"args": map[string]interface{}{},
	}
	globalEvents = append(globalEvents, event)
}

func appendProfileChunkEvent(topNodes []*ProfileNode, times []int, newNodes []*ProfileNode, t int64) {
	N := len(topNodes)
	samples := make([]interface{}, N)
	timeDeltas := make([]interface{}, N)
	lines := make([]interface{}, N)

	for i := range samples {
		samples[i] = topNodes[i].ID
	}

	for i := range timeDeltas {
		timeDeltas[i] = times[i] / int(samplerTimeUnit)
	}
	for i := range lines {
		lines[i] = 1
	}
	cpuProfile := map[string]interface{}{
		"samples": samples,
	}

	if len(newNodes) > 0 {
		cpuProfile["nodes"] = newNodes
	}

	data := map[string]interface{}{

		"timeDeltas": timeDeltas,
		"lines":      lines,
	}
	data["cpuProfile"] = cpuProfile

	event := map[string]interface{}{
		"pid":  1337,
		"tid":  11504,
		"ts":   (commonTimeAdded + t) / samplerTimeUnit,
		"ph":   "P",
		"cat":  "disabled-by-default-v8.cpu_profiler",
		"name": "ProfileChunk",
		"id":   "0x4",
		"tts":  t / samplerTimeUnit,
		"args": map[string]interface{}{
			"data": data,
		},
	}
	profileEvents = append(profileEvents, event)
}
