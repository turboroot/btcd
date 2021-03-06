// Copyright (c) 2013 Conformal Systems LLC.
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"github.com/conformal/btcchain"
	"github.com/conformal/btcdb"
	"github.com/conformal/btcscript"
	"github.com/conformal/btcwire"
	"github.com/conformal/seelog"
	"math/rand"
	"net"
	"os"
	"runtime"
	"strconv"
	"time"
)

// These constants are used by the dns seed code to pick a random last seen
// time.
const (
	secondsIn3Days int32 = 24 * 60 * 60 * 3
	secondsIn4Days int32 = 24 * 60 * 60 * 4
)

var (
	log seelog.LoggerInterface = seelog.Disabled
	cfg *config
)

// newLogger creates a new seelog logger using the provided logging level and
// log message prefix.
func newLogger(level string, prefix string) seelog.LoggerInterface {
	fmtstring := `
	<seelog type="adaptive" mininterval="2000000" maxinterval="100000000"
		critmsgcount="500" minlevel="%s">
		<outputs formatid="all">
			<console/>
		</outputs>
		<formats>
			<format id="all" format="[%%Time %%Date] [%%LEV] [%s] %%Msg%%n" />
		</formats>
	</seelog>`
	config := fmt.Sprintf(fmtstring, level, prefix)

	logger, err := seelog.LoggerFromConfigAsString(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create logger: %v", err)
		os.Exit(1)
	}

	return logger
}

// useLogger sets the btcd logger to the passed logger.
func useLogger(logger seelog.LoggerInterface) {
	log = logger
}

// setLogLevel sets the log level for the logging system.  It initialises a
// logger for each subsystem at the provided level.
func setLogLevel(logLevel string) []seelog.LoggerInterface {
	var loggers []seelog.LoggerInterface

	// Define sub-systems.
	subSystems := []struct {
		level     string
		prefix    string
		useLogger func(seelog.LoggerInterface)
	}{
		{logLevel, "BTCD", useLogger},
		{logLevel, "BCDB", btcdb.UseLogger},
		{logLevel, "CHAN", btcchain.UseLogger},
		{logLevel, "SCRP", btcscript.UseLogger},
	}

	// Configure all sub-systems with new loggers while keeping track of
	// the created loggers to return so they can be flushed.
	for _, s := range subSystems {
		newLog := newLogger(s.level, s.prefix)
		loggers = append(loggers, newLog)
		s.useLogger(newLog)
	}

	return loggers
}

// btcdMain is the real main function for btcd.  It is necessary to work around
// the fact that deferred functions do not run when os.Exit() is called.
func btcdMain() error {
	// Initialize logging and setup deferred flushing to ensure all
	// outstanding messages are written on shutdown.
	loggers := setLogLevel(defaultLogLevel)
	defer func() {
		for _, logger := range loggers {
			logger.Flush()
		}
	}()

	// Load configuration and parse command line.
	tcfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	cfg = tcfg

	// Change the logging level if needed.
	if cfg.DebugLevel != defaultLogLevel {
		loggers = setLogLevel(cfg.DebugLevel)
	}

	// Load the block database.
	db, err := loadBlockDB()
	if err != nil {
		log.Errorf("%v", err)
		return err
	}
	defer db.Close()

	// Ensure the database is sync'd and closed on Ctrl+C.
	addInterruptHandler(func() {
		db.RollbackClose()
	})

	// Create server and start it.
	listenAddr := net.JoinHostPort("", cfg.Port)
	server, err := newServer(listenAddr, db, activeNetParams.btcnet)
	if err != nil {
		log.Errorf("Unable to start server on %v", listenAddr)
		log.Errorf("%v", err)
		return err
	}
	server.Start()

	// only ask dns for peers if we don't have a list of initial seeds.
	if !cfg.DisableDNSSeed {
		proxy := ""
		if cfg.Proxy != "" && cfg.UseTor {
			proxy = cfg.Proxy
		}
		seedpeers := dnsDiscover(activeNetParams.dnsSeeds, proxy)
		addresses := make([]*btcwire.NetAddress, len(seedpeers))
		// if this errors then we have *real* problems
		intPort, _ := strconv.Atoi(activeNetParams.peerPort)
		for i, peer := range seedpeers {
			addresses[i] = new(btcwire.NetAddress)
			addresses[i].SetAddress(peer, uint16(intPort))
			// bitcoind seeds with addresses from
			// a time randomly selected between 3
			// and 7 days ago.
			addresses[i].Timestamp = time.Now().Add(-1 *
				time.Second * time.Duration(secondsIn3Days+
				rand.Int31n(secondsIn4Days)))
		}

		server.addrManager.AddAddresses(addresses)
		// XXX if this is empty do we want to use hardcoded
		// XXX peers like bitcoind does?
	}

	peers := cfg.ConnectPeers
	if len(peers) == 0 {
		peers = cfg.AddPeers
	}
	// Connect to initial peers.
	for _, addr := range peers {
		// Connect to peer and add it to the server.
		server.ConnectPeerAsync(addr, true)
	}

	server.WaitForShutdown()
	return nil
}

func main() {
	// Use all processor cores.
	runtime.GOMAXPROCS(runtime.NumCPU())

	// Work around defer not working after os.Exit()
	err := btcdMain()
	if err != nil {
		os.Exit(1)
	}
}
