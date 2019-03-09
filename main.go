/*
   rtldavis, an rtl-sdr receiver for Davis Instruments weather stations.
   Copyright (C) 2015  Douglas Hall

   This program is free software: you can redistribute it and/or modify
   it under the terms of the GNU General Public License as published by
   the Free Software Foundation, either version 3 of the License, or
   (at your option) any later version.

   This program is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
   GNU General Public License for more details.

   You should have received a copy of the GNU General Public License
   along with this program.  If not, see <http://www.gnu.org/licenses/>.

	Modified by Luc Heijst - March 2019
	Added: Multi-channel hopping
	Added: options -ex, -un, -tf, -tr
	Removed: option -id
*/
package main

import (
	"flag"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"time"

	"github.com/lheijst/rtldavis/protocol"
	"github.com/jpoirier/gortlsdr"
)

var (
	// program settings
	transmitterId	*int		// bitwise combination of transmitters
	ex				int			// -ex = extra loopTime in msex
	transmitterFreq	*string		// -tf = transmitter frequencies, EU, or US
	undefined		*bool		// -un = log undefined signals
	verbose 		*bool		//
	verboseLogger 	*log.Logger	//

	// general
	actChan			[8]int		// list with actual channels (0-7); 
								//   next values are zero (non-meaning)
	msgIdToChan		[]int		// msgIdToChan[id] is pointer to channel in actChan; 
								//   non-defined id's have ptr value 9
	expectedChanPtr	int			// pointer to actChan of next expected msg.Data
	curTime			int64		// current UTC-nanoseconds
	maxFreq			int			// number of frequencies (EU=5, US=51)
	maxChan			int			// number of defined (=actual) channels

	// per channel (index is actChan[ch])
	chLastVisits	[8]int64	// last visit times in UTC-nanoseconds
	chNextVisits	[8]int64	// next visit times (future) in UTC-nanoseconds
	chNextVisitsFmt	[8]string	// next visit times (future) in UTC-time-string
	chTotMsgs		[8]int		// total received messages since startup
	chTotMiss		[8]int		// total missed messages since startup
	chAlarmCnts		[8]int		// numbers of missed-counts-in-a-row
	chLastHops		[8]int		// last hop channel-ids (sequential order)
	chNextHops		[8]int		// next hop channel-ids (sequential order)

	// per id (index is msg.ID)
	idLoopPeriods 	[8]time.Duration // durations of one loop (higher IDs: longer durations)
	idUndefs		[8]int		// number of received messages of undefined id's since startup 

	// totals
	totMsg			int			// total received messages since startup
	totMis			int			// total missed messages since startup
	totInit			int			// total of init procedures since startup (first not counted)

	// hop and channel-frequency
	loopTimer		time.Time	// time when next hop sequence will time-out
	loopPeriod		time.Duration // period since now when next hop sequence will time-out
	actHopChanIdx	int			// channel-id of actual hop sequence (EU: 0-4, US: 0-50)
	nextHopChan		int			// channel-id of next hop
	freqError		int			// frequency error of last hop

	// controll
	initTransmitrs	bool		// start an init session to synchronize all defined channels
	handleNxtPacket	bool		// start preparation for reading next data packet
	saveFreqError	bool		// save last freqError when true

	// init
	visitCount		int			// number of different active channels seen during init

	// msg handling
	lastRecMsg		string		// string of last received raw code

)


func init() {
	VERSION := "0.7"
var (
	tr		int
	mask	int
)
	msgIdToChan = []int {9, 9, 9, 9, 9, 9, 9, 9, }	// preset with 9 (= undefined)

	log.SetFlags(log.Lmicroseconds)
	rand.Seed(time.Now().UnixNano())

	// read program settings
	flag.IntVar(&tr, "tr", 1, "transmitters to listen for: tr1=1, tr2=2, tr3=4, tr4=8, tr5=16 tr6=32, tr7=64, tr8=128")
	flag.IntVar(&ex, "ex", 0, "extra loopPeriod time in msec")
	transmitterFreq = flag.String("tf", "EU", "transmitter frequencies: EU or US")
    undefined = flag.Bool("u", false, "log undefined signals")
	verbose = flag.Bool("v", true, "log extra information to /dev/stderr")

	flag.Parse()

	verboseLogger = log.New(ioutil.Discard, "", log.Lshortfile|log.Lmicroseconds)
	if *verbose {
		verboseLogger.SetOutput(os.Stderr)
	}
	log.Printf("rtldavis.go VERSION=%s\n", VERSION)
	// convert tranceiver code to act channels
	mask = 1
	for i := range actChan {
		if tr & mask != 0 {
			actChan[maxChan] = i
			msgIdToChan[i] = maxChan
			maxChan += 1
		}
		mask = mask << 1
	}
	transmitterId = &actChan[maxChan-1]  // transmitterId of last active transmitter
	log.Printf("tr=%d actChan=%d maxChan=%d *transmitterId=%d msgIdToChan=%d\n", tr, actChan[0:maxChan], maxChan, *transmitterId, msgIdToChan) 

	// Preset loopperiods per id
	idLoopPeriods[0] = 2562500 * time.Microsecond
	for i := 1; i < 8; i++ {
		idLoopPeriods[i] = idLoopPeriods[i-1] + 62500 * time.Microsecond
	}
	log.Printf("idLoopPeriods=%d\n", idLoopPeriods)

}

func main() {
	p := protocol.NewParser(14, *transmitterId, *transmitterFreq)
	p.Cfg.Log()

	fs := p.Cfg.SampleRate

	dev, err := rtlsdr.Open(0)
	if err != nil {
		log.Fatal(err)
	}

	hop := p.SetHop(0)	// start program with first hop frequency
	verboseLogger.Println(hop)
	if err := dev.SetCenterFreq(hop.ChannelFreq); err != nil {
		log.Fatal(err)
	}

	if err := dev.SetSampleRate(fs); err != nil {
		log.Fatal(err)
	}

	if err := dev.SetTunerGainMode(false); err != nil {
		log.Fatal(err)
	}

	if err := dev.ResetBuffer(); err != nil {
		log.Fatal(err)
	}

	in, out := io.Pipe()

	go dev.ReadAsync(func(buf []byte) {
		out.Write(buf)
	}, nil, 1, p.Cfg.BlockSize2)

	// Handle frequency hops concurrently since the callback will stall if we
	// stop reading to hop.
	nextHop := make(chan protocol.Hop, 1)
	go func() {
		for hop := range nextHop {
			verboseLogger.Printf("Hop: %s\n", hop)
			actHopChanIdx = hop.ChannelIdx
			if err := dev.SetCenterFreq(hop.ChannelFreq); err != nil {
				//log.Fatal(err)
				verboseLogger.Printf("SetCenterFreq error: %s\n", err)
				verboseLogger.Printf("SetCenterFreq = %s\n", hop.ChannelFreq)
			}
		}
	}()

	defer func() {
		in.Close()
		out.Close()
		dev.CancelAsync()
		dev.Close()
		os.Exit(0)
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, os.Kill)

	block := make([]byte, p.Cfg.BlockSize2)
	initTransmitrs = true
	maxFreq = p.ChannelCount

	// Set the idLoopPeriods for one full rotation of the pattern + 1. 
	loopPeriod = time.Duration(maxFreq +1) * idLoopPeriods[actChan[maxChan-1]]
	loopTimer := time.After(loopPeriod)  // loopTimer of highest transmitter
	log.Printf("Init loopTimer and wait for messages on channel 0: loopTimer=%d idLoopPeriods=%d loopPeriod=%d\n", loopTimer, idLoopPeriods[actChan[maxChan-1]], loopPeriod)

	for {
		select {
		case <-sig:
			return
		case <-loopTimer:
			// If the loopTimer has expired one of two things has happened:
			//	 1: We've missed a message.
			//	 2: We've waited for sync and nothing has happened for a
			//		full cycle of the pattern.

			log.Printf("ID:%d packet missed", actChan[expectedChanPtr])
			if !initTransmitrs {
				// packet missed
				curTime = time.Now().UnixNano()
				// forget the handling of this channel; update lastVisitTime as if the packet was received
				chLastVisits[expectedChanPtr] += int64(idLoopPeriods[actChan[expectedChanPtr]])
				// update chLastHops as if the packet was received
				chLastHops[expectedChanPtr] = (chLastHops[expectedChanPtr] + 1) % maxFreq
				// increase missed counters
				totMis++
				chTotMiss[expectedChanPtr]++
				chAlarmCnts[expectedChanPtr]++
				for i := 0; i < maxChan; i++ {
					if chAlarmCnts[i] > 3 {
						chAlarmCnts[i] = 0   // reset current alarm count
						initTransmitrs = true
					}
				}
			}
			// test again; situation may have changed
			if !initTransmitrs {
				HandleNextHopChannel()
				nextHopChan = p.SeqToHop(chNextHops[expectedChanPtr])
				// loopPeriod standard increased with 200 msec
				loopPeriod = time.Duration(chNextVisits[expectedChanPtr] - curTime + int64(62500 * time.Microsecond) + int64(ex * 1000000))
				loopTimer = time.After(loopPeriod)
				log.Printf("loopTimer expired; hop to channelIdx: %d %s ID=%d\n", nextHopChan, chNextVisitsFmt[expectedChanPtr], actChan[expectedChanPtr])
				nextHop <- p.SetHop(chNextHops[expectedChanPtr])
			} else {
				//log.Printf("INIT: reset chLastVisits")
				for i := 0; i < maxChan; i++ {
					chLastVisits[i] = 0
				}
				visitCount = 0
				totInit++
				loopPeriod = time.Duration(maxFreq +1) * idLoopPeriods[actChan[maxChan-1]]
				loopTimer = time.After(loopPeriod)
				log.Printf("Init channels: wait for messages")
				nextHop <- p.SetHop(0)

			}

		default:
			in.Read(block)
			handleNxtPacket = false
			for _, msg := range p.Parse(p.Demodulate(block)) {
				curTime = time.Now().UnixNano()
				log.Printf("msg.Data: %02X\n", msg.Data)
				// Keep track of duplicate packets
				seen := string(msg.Data)
				if seen == lastRecMsg {
					log.Printf("duplicate packet: %02X\n", msg.Data)
					continue  // read next message
				}
				lastRecMsg = seen
				// check if msg comes from undefined sensor
				if msgIdToChan[int(msg.ID)] == 9 {
					if *undefined {
						log.Printf("undefined: %02X ID=%d\n", msg.Data, msg.ID)
					}
					idUndefs[int(msg.ID)]++
					continue  // read next message
				} else {
					totMsg++
					chTotMsgs[msgIdToChan[int(msg.ID)]]++
					chAlarmCnts[msgIdToChan[int(msg.ID)]] = 0  // reset current missed count
					if initTransmitrs {
						log.Printf("init channels, msg.ID=%d msgIdToChan=%d chLastVisits=%d", msg.ID, msgIdToChan[int(msg.ID)], chLastVisits[0:maxChan])
						if chLastVisits[msgIdToChan[int(msg.ID)]] == 0 {
							visitCount +=1
							chLastVisits[msgIdToChan[int(msg.ID)]] = curTime
							chLastHops[msgIdToChan[int(msg.ID)]] = p.HopToSeq(actHopChanIdx)
							log.Printf("NEW TRANSMITTER: msg.ID=%d chLastVisits=%d visitCount=%d chLastHops=%d\n", msg.ID, chLastVisits[0:maxChan], visitCount, chLastHops[0:maxChan])
							if visitCount == maxChan {
								if maxChan > 1 {
									log.Printf("ALL TRANSMITTERS SEEN: chLastVisits=%d visitCount=%d chLastHops=%d\n", chLastVisits[0:maxChan], visitCount, chLastHops[0:maxChan])
								}
								log.Printf("last seen msg.Data: %02X\n", msg.Data)
								initTransmitrs = false
								handleNxtPacket = true
							}
						} else {
							chLastVisits[msgIdToChan[int(msg.ID)]] = curTime  // update chLastVisits timer 
						}
					} else {
						//log.Printf("normal hopping")
						chLastHops[msgIdToChan[int(msg.ID)]] = p.HopToSeq(actHopChanIdx)
						chLastVisits[msgIdToChan[int(msg.ID)]] = curTime
						if *undefined {
							log.Printf("%02X %d %d %d %d msg.ID=%d packets:%d missed:%d %d inits:%d undefineds:%d\n", 
								msg.Data, chTotMsgs[0], chTotMsgs[1], chTotMsgs[2], chTotMsgs[3], msg.ID, 
								totMsg, totMis, chTotMiss[0:maxChan], totInit, idUndefs)
						} else {
							log.Printf("%02X %d %d %d %d msg.ID=%d packets:%d missed:%d %d inits:%d\n", 
								msg.Data, chTotMsgs[0], chTotMsgs[1], chTotMsgs[2], chTotMsgs[3], msg.ID, 
								totMsg, totMis, chTotMiss[0:maxChan], totInit)
						}
						handleNxtPacket = true
					}
				}
			}
			if handleNxtPacket {
				HandleNextHopChannel()
				nextHopChan = p.SeqToHop(chNextHops[expectedChanPtr])
				// loopPeriod standard increased with 200 msec
				loopPeriod = time.Duration(chNextVisits[expectedChanPtr] - curTime + int64(62500 * time.Microsecond) + int64(ex * 1000000))
				loopTimer = time.After(loopPeriod)
				log.Printf("Hop to channelIdx: %d %s ID=%d\n", nextHopChan, chNextVisitsFmt[expectedChanPtr], actChan[expectedChanPtr])
				nextHop <- p.SetHop(chNextHops[expectedChanPtr])
			}
		}
	}
}

func convTim(unixTime int64) (t time.Time) {
	return time.Unix(0, unixTime * int64(time.Nanosecond))
	}

func HandleNextHopChannel() {
	// calculate chNextVisits times
	for i := 0; i < 8; i++ {
		chNextVisits[i] = 0
		chNextHops[i] = chLastHops[i]
	}
	// check lastVisits; zero values should not happen,
	// but when it does the program will be very busy (c.q. hang)
	for i := 0; i < maxChan; i++ {
		if chLastVisits[i] == 0 {
			log.Printf("ERROR: chLastVisits[%d] should not be zero!", i)
			chLastVisits[i] = curTime  // workaround to get further
		}
	}
	for i := 0; i < 8; i++ {
		if msgIdToChan[i] < 9 {
			for chNextVisits[msgIdToChan[i]] = chLastVisits[msgIdToChan[i]]; chNextVisits[msgIdToChan[i]] <= curTime; chNextVisits[msgIdToChan[i]] += int64(idLoopPeriods[actChan[msgIdToChan[i]]]) {
				chNextHops[msgIdToChan[i]] = (chNextHops[msgIdToChan[i]] + 1) % maxFreq
			}
		}
	}
	expectedChanPtr = Min(chNextVisits[0:maxChan])
	// for printing purposes only
	for i := 0; i < maxChan; i++ {
		chNextVisitsFmt[i] = convTim(chNextVisits[i]).Format("15:04:05.999999")
	}
}

func Min(values []int64) (ptr int) {
	var min int64
	min = values[0]
	for i := 0; i < maxChan; i++ {
		if (values[i] < min) {
			min = values[i]
			ptr = i
		}
	}
	return ptr
}
