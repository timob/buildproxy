package main

import (
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	yaml "gopkg.in/yaml.v2"
)

type CmdBlock struct {
	Argv    []string `yaml:"argv"`
	Env     []string `yaml:"env"`
	WorkDir string   `yaml:"work_dir"`
}

func (c *CmdBlock) GetExecCmd() *exec.Cmd {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	cmd := &exec.Cmd{}
	// Set CWD for new process by workdir if it's absolute, if it's relative then relative to CWD or CWD if not specified. 
	if filepath.IsAbs(c.WorkDir) {
		cmd.Dir = c.WorkDir
	} else if c.WorkDir != "" {
		cmd.Dir = filepath.Join(cwd, c.WorkDir)
	} else {
		cmd.Dir = cwd
	}
	cmd.Args = c.Argv
	if len(cmd.Args) > 0 {
		// Look for cmd.Args[0] in its full path, work dir or PATH ; and set cmd.Path.
		if strings.Contains(cmd.Args[0], string(filepath.Separator)) {
			cmd.Path = cmd.Args[0]
		} else {
			if _, err := os.Stat(filepath.Join(cmd.Dir, cmd.Args[0])); err == nil {
				cmd.Path = filepath.Join(cmd.Dir, cmd.Args[0])
			} else {
				fullPth, err := exec.LookPath(cmd.Args[0])
				if err == nil {
					cmd.Path = fullPth
				} else {
					log.Fatal("ERROR: can't find executable " + cmd.Args[0])
				}
			}
		}
	}
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

type ConfigStanza struct {
	Name           string   `yaml:"name"`
	StartCommand   CmdBlock `yaml:"start_command"`
	BuildCommand   CmdBlock `yaml:"build_command"`
	DestinationURL string   `yaml:"destination_url"`
	ExcludePaths   []string `yaml:"exclude_paths"`
	FileExtensions []string `yaml:"file_extensions"`
	ListenAddress  string   `yaml:"listen_address"`
	WatchPath      string   `yaml:"watch_path"`
}

const (
	DEFAULT_CONFIG_FILE_NAME = ".buildproxy.yaml"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	var homeDir string
	if v, err := user.Current(); err == nil {
		homeDir = v.HomeDir
	} else {
		log.Fatal(err)
	}
	var cfgFile string
	flag.StringVar(&cfgFile, "config", filepath.Join(homeDir, DEFAULT_CONFIG_FILE_NAME), "config file to load")
	flag.Parse()

	cfgData, err := ioutil.ReadFile(cfgFile)
	if err != nil {
		log.Fatal(err)
	}

	var config []ConfigStanza
	if err := yaml.Unmarshal(cfgData, &config); err != nil {
		log.Fatal(err)
	}

	if len(flag.Args()) < 1 {
		log.Fatal("ERROR: Give the build proxy name as argument to command")
	}
	selectedName := flag.Args()[0]
	var stanza ConfigStanza
	for _, v := range config {
		if v.Name == selectedName {
			stanza = v
		}
	}
	if stanza.Name == "" {
		log.Fatalf("ERROR: build proxy configuration for %s not found in %s", selectedName, cfgFile)
	}

	if err := os.Chdir(stanza.WatchPath); err != nil {
		log.Fatal(err)
	}

	parsedUrl, err := url.Parse(stanza.DestinationURL)
	if err != nil {
		log.Fatal(err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	// need make sure ready to start recving events before adding?
	fileChanged := true
	go func() {
		for ev := range watcher.Events {
			for _, extension := range stanza.FileExtensions {
				if strings.HasSuffix(ev.Name, extension) {
					fileChanged = true
					break
				}
			}
		}
	}()

	err = filepath.Walk(
		stanza.WatchPath,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				for _, exclude := range stanza.ExcludePaths {
					if path == filepath.Join(stanza.WatchPath, exclude) {
						return nil
					}
				}
				watcher.Add(path)
			}
			return nil
		},
	)
	if err != nil {
		log.Println(err)
		return
	}

	var backend *exec.Cmd
	doWait := true
	procSig := syscall.SIGTERM
	if runtime.GOOS == "windows" {
		doWait = false
		procSig = syscall.SIGKILL
	}

	proxy := httputil.NewSingleHostReverseProxy(parsedUrl)
	reqLock := &sync.Mutex{}
	handler := func(rs http.ResponseWriter, rq *http.Request) {
		reqLock.Lock()
		defer reqLock.Unlock()
		defer func() { fileChanged = false }()
		if fileChanged == true {
			log.Println("files changed rebuilding backend")
			// Signal back end process to exit before rebuilding.
			if backend != nil && backend.Process != nil {
				if err := backend.Process.Signal(procSig); err != nil {
					log.Println("error stopping backend: " + err.Error())
				}
				if doWait {
					backend.Process.Wait()
				} else {
					time.Sleep(time.Second)
				}
				backend = nil
			}

			log.Println("building...")
			cmd := stanza.BuildCommand.GetExecCmd()
			err := cmd.Run()
			if err != nil {
				log.Println("error building: " + err.Error())
				rs.WriteHeader(500)
				return
			}

			log.Println("starting backend...")
			backend = stanza.StartCommand.GetExecCmd()
			err = backend.Start()
			if err != nil {
				log.Println("error starting backend: " + err.Error())
				rs.WriteHeader(500)
				return
			}
			time.Sleep(time.Second)
		}
		rs.Header().Add("Proxy", "buildproxy")
		proxy.ServeHTTP(rs, rq)
	}

	c := make(chan os.Signal)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGQUIT, os.Interrupt)

	// Run webserver in background.
	go func() {
		if err := http.ListenAndServe(stanza.ListenAddress, http.HandlerFunc(handler)); err != nil {
			log.Println(err)
			proc, err := os.FindProcess(os.Getpid())
			if err != nil {
				log.Println(err)
				return
			}
			proc.Signal(os.Interrupt)
		}
	}()

	log.Println("INFO: listening on " + stanza.ListenAddress)
	recv := <-c
	log.Println("got signal " + recv.String())
	if backend != nil && backend.Process != nil {
		log.Println("stopping backend")
		backend.Process.Signal(procSig)
	}
}
