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

type ConfigStanza struct {
	Name           string   `yaml:"name"`
	StartCommand   string   `yaml:"start_command"`
	BuildCommand   string   `yaml:"build_command"`
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
			parts := strings.Split(stanza.BuildCommand, " ")
			cmd := exec.Command(parts[0], parts[1:]...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			err := cmd.Run()
			if err != nil {
				log.Println("error building: " + err.Error())
				rs.WriteHeader(500)
				return
			}

			log.Println("starting backend...")
			parts = strings.Split(stanza.StartCommand, " ")
			backend = exec.Command(parts[0], parts[1:]...)
			backend.Stdout = os.Stdout
			backend.Stderr = os.Stderr
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

	recv := <-c
	log.Println("got signal " + recv.String())
	if backend != nil && backend.Process != nil {
		log.Println("stopping backend")
		backend.Process.Signal(procSig)
	}
}
