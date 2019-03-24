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
	Added: options 	-ex, -u, -fc, -tf, -tr, 
			-startfreq, -endfreq, -stepfreq 
	Removed: option	-id, -v
*/
package main

import (
	"flag"
	"io"
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
	ex				int			// -ex = extra loopTime in msex
	fc				int			// -fc = frequency correction for all channels
	transmitterFreq	*string		// -tf = transmitter frequencies, EU, or US
	undefined		*bool		// -un = log undefined signals

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
	chTotMsgs		[8]int		// total received messages since startup
	chAlarmCnts		[8]int		// numbers of missed-counts-in-a-row
	chLastHops		[8]int		// last hop channel-ids (sequential order)
	chNextHops		[8]int		// next hop channel-ids (sequential order)
	chMissPerFreq	[8][5]int	// transmitter missed per frequency channel; temporary (not for US freq)

	// per id (index is msg.ID)
	idLoopPeriods 	[8]time.Duration // durations of one loop (higher IDs: longer durations)
	idUndefs		[8]int		// number of received messages of undefined id's since startup 

	// totals
	totInit			int			// total of init procedures since startup (first not counted)

	// hop and channel-frequency
	loopTimer		time.Time	// time when next hop sequence will time-out
	loopPeriod		time.Duration // period since now when next hop sequence will time-out
	actHopChanIdx	int			// channel-id of actual hop sequence (EU: 0-4, US: 0-50)
	nextHopChan		int			// channel-id of next hop
	channelFreq		int			// frequency of the channel to transmit
	freqError		int			// frequency error of last hop
	freqCorrection	int			// frequencyCorrection (average freqError per transmitter per channel)

	// controll
	initTransmitrs	bool		// start an init session to synchronize all defined channels
	handleNxtPacket	bool		// start preparation for reading next data packet

	// init
	visitCount		int			// number of different active channels seen during init

	// msg handling
	lastRecMsg		string		// string of last received raw code

	// test
	testFreq		bool
	startFreq		int
	endFreq			int
	stepFreq		int
	testChannelFreq	int
	testNumber		int

)


func init() {
	VERSION := "0.10"
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
	flag.IntVar(&fc, "fc", 0, "frequency correction in Hz for all channels")
	flag.IntVar(&startFreq, "startfreq", 0, "test")
	flag.IntVar(&endFreq, "endfreq", 0, "test")
	flag.IntVar(&stepFreq, "stepfreq", 0, "test")
	transmitterFreq = flag.String("tf", "EU", "transmitter frequencies: EU or US")
	undefined = flag.Bool("u", false, "log undefined signals")

	flag.Parse()

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
	log.Printf("tr=%d fc=%d ex=%d actChan=%d maxChan=%d\n", tr, fc, ex, actChan[0:maxChan], maxChan) 

	// Preset loopperiods per id
	idLoopPeriods[0] = 2562500 * time.Microsecond
	for i := 1; i < 8; i++ {
		idLoopPeriods[i] = idLoopPeriods[i-1] + 62500 * time.Microsecond
	}

	// check if test
	if startFreq != 0 && endFreq !=0 && stepFreq != 0 {
		log.Printf("TEST: startFreq=%d endFreq=%d stepFreq=%d\n", startFreq, endFreq, stepFreq)
		testFreq = true
		testChannelFreq = startFreq - stepFreq
	}

}

func main() {
	p := protocol.NewParser(14, *transmitterFreq)
	p.Cfg.Log()

	fs := p.Cfg.SampleRate

	dev, err := rtlsdr.Open(0)
	if err != nil {
		log.Fatal(err)
	}

	hop := p.SetHop(0)	// start program with first hop frequency
	log.Println(hop)
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
			freqError = hop.FreqError
			if testFreq {
				freqCorrection = 0
				testChannelFreq = testChannelFreq + stepFreq
				testNumber++ 
				if testChannelFreq > endFreq {
					endmsg := "Test reached endfreq; test ended"
					log.Fatal(endmsg)
				}
				channelFreq = testChannelFreq
			} else {
				freqCorrection = freqError
				log.Printf("Hop: %s\n", hop)
				actHopChanIdx = hop.ChannelIdx
				channelFreq = hop.ChannelFreq
			}
			if err := dev.SetCenterFreq(channelFreq + freqCorrection + fc); err != nil {
				//log.Fatal(err)  // no reason top stop program for one error
				log.Printf("SetCenterFreq: %d error: %s\n", hop.ChannelFreq, err)
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
	log.Printf("Init channels: wait max %d seconds for a message of each transmitter", loopPeriod/1000000000)

	for {
		select {
		case <-sig:
			return
		case <-loopTimer:
			// If the loopTimer has expired one of two things has happened:
			//	 1: We've missed a message.
			//	 2: We've waited for sync and nothing has happened for a
			//		full cycle of the pattern.

			if testFreq {
				if testNumber > 0 {
					log.Printf("TESTFREQ %d: Frequency %d: NOK", testNumber, testChannelFreq)
				}
				loopPeriod = time.Duration(maxFreq +1) * idLoopPeriods[actChan[maxChan-1]]
				loopTimer = time.After(loopPeriod)
				nextHop <- p.SetHop(0)
			} else {
				if !initTransmitrs {
					// packet missed
					curTime = time.Now().UnixNano()
					// forget the handling of this channel; update lastVisitTime as if the packet was received
					chLastVisits[expectedChanPtr] += int64(idLoopPeriods[actChan[expectedChanPtr]])
					// update chLastHops as if the packet was received
					chLastHops[expectedChanPtr] = (chLastHops[expectedChanPtr] + 1) % maxFreq
					// increase missed counters
					chAlarmCnts[expectedChanPtr]++
					chMissPerFreq[actChan[expectedChanPtr]][nextHopChan]++
					log.Printf("ID:%d packet missed (%d), missed per freq: %d", actChan[expectedChanPtr], chAlarmCnts[expectedChanPtr], chMissPerFreq[actChan[expectedChanPtr]])
					for i := 0; i < maxChan; i++ {
						if chAlarmCnts[i] > 5 {
							chAlarmCnts[i] = 0   // reset current alarm count
							initTransmitrs = true
						}
					}
				}
				// test again; situation may have changed
				if !initTransmitrs {
					HandleNextHopChannel()
					nextHopChan = chNextHops[expectedChanPtr]
					loopPeriod = time.Duration(chNextVisits[expectedChanPtr] - curTime + int64(62500 * time.Microsecond) + int64(ex * 1000000))
					loopTimer = time.After(loopPeriod)
					nextHop <- p.SetHop(nextHopChan)
				} else {
					// reset chLastVisits
					for i := 0; i < maxChan; i++ {
						chLastVisits[i] = 0
					}
					visitCount = 0
					totInit++
					loopPeriod = time.Duration(maxFreq +1) * idLoopPeriods[actChan[maxChan-1]]
					loopTimer = time.After(loopPeriod)
					log.Printf("Init channels: wait max %d seconds for a message of each transmitter", loopPeriod/1000000000)
					nextHop <- p.SetHop(0)
				}
			}

		default:
			in.Read(block)
			handleNxtPacket = false
			for _, msg := range p.Parse(p.Demodulate(block)) {
				if testFreq {
					if testNumber > 0 {
						if msgIdToChan[int(msg.ID)] != 9 {
							log.Printf("TESTFREQ %d: Frequency %d (freqError=%d): OK, msg.data: %02X\n", testNumber, testChannelFreq, freqError, msg.Data)
							loopPeriod = time.Duration(maxFreq +1) * idLoopPeriods[actChan[maxChan-1]]
							loopTimer = time.After(loopPeriod)
							nextHop <- p.SetHop(0)
						}
					}
					continue  // read next message
				}
				curTime = time.Now().UnixNano()
				//log.Printf("msg.Data: %02X\n", msg.Data)
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
					chTotMsgs[msgIdToChan[int(msg.ID)]]++
					chAlarmCnts[msgIdToChan[int(msg.ID)]] = 0  // reset current missed count
					if initTransmitrs {
						if chLastVisits[msgIdToChan[int(msg.ID)]] == 0 {
							visitCount +=1
							chLastVisits[msgIdToChan[int(msg.ID)]] = curTime
							chLastHops[msgIdToChan[int(msg.ID)]] = p.HopToSeq(actHopChanIdx)
							log.Printf("TRANSMITTER %d SEEN\n", msg.ID)
							if visitCount == maxChan {
								if maxChan > 1 {
									log.Printf("ALL TRANSMITTERS SEEN")
								}
								initTransmitrs = false
								handleNxtPacket = true
							}
						} else {
							chLastVisits[msgIdToChan[int(msg.ID)]] = curTime  // update chLastVisits timer 
						}
					} else {
						// normal hopping
						chLastHops[msgIdToChan[int(msg.ID)]] = p.HopToSeq(actHopChanIdx)
						chLastVisits[msgIdToChan[int(msg.ID)]] = curTime
						if *undefined {
							log.Printf("%02X %d %d %d %d %d msg.ID=%d undefined:%d\n", 
								msg.Data, chTotMsgs[0], chTotMsgs[1], chTotMsgs[2], chTotMsgs[3], totInit, msg.ID, idUndefs)
						} else {
							log.Printf("%02X %d %d %d %d %d msg.ID=%d\n", 
								msg.Data, chTotMsgs[0], chTotMsgs[1], chTotMsgs[2], chTotMsgs[3], totInit, msg.ID) 
						}
						handleNxtPacket = true
					}
				}
			}
			if handleNxtPacket {
				HandleNextHopChannel()
				nextHopChan = chNextHops[expectedChanPtr]
				loopPeriod = time.Duration(chNextVisits[expectedChanPtr] - curTime + int64(62500 * time.Microsecond) + int64(ex * 1000000))
				loopTimer = time.After(loopPeriod)
				nextHop <- p.SetHop(nextHopChan)
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
