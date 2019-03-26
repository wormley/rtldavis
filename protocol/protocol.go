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
   Added: EU-frequencies
   Removed: frequency correction
   Removed: parsing
   VERSION: 0.10
*/
package protocol

import (
	"fmt"
//	"log"
	"math"
	"math/rand"

	"github.com/lheijst/rtldavis/crc"
	"github.com/lheijst/rtldavis/dsp"
)

func NewPacketConfig(symbolLength int) (cfg dsp.PacketConfig) {
	return dsp.NewPacketConfig(
		19200,
		14,
		16,
		80,
		"1100101110001001",
	)
}

type Parser struct {
	dsp.Demodulator
	crc.CRC
	Cfg dsp.PacketConfig
	ChannelCount 	int
	channels		[]int
	hopIdx			int
	hopPattern		[]int
	reverseHopPatrn []int
	freqError		int
	chfreq			int
	freqerrTrChList [8][51][10]int
	freqerrTrChPtr	[8][51]int
	freqerrTrChSum	[8][51]int
	maxTrChList		int
}

func NewParser(symbolLength int, tf string) (p Parser) {
	p.Cfg = NewPacketConfig(symbolLength)
	p.Demodulator = dsp.NewDemodulator(&p.Cfg)
	p.CRC = crc.NewCRC("CCITT-16", 0, 0x1021, 0)
	p.maxTrChList = 10

	if tf == "EU" {
		p.channels = []int{ 
			868077250, 868197250, 868317250, 868437250, 868557250, // EU test 20190324
		}
		p.ChannelCount = len(p.channels)
		p.hopIdx = rand.Intn(p.ChannelCount)
		p.hopPattern = []int{
			0, 2, 4, 1, 3,   
		}
		p.reverseHopPatrn = []int{
			0, 3, 1, 4, 2,   
		}
	} else {
		p.channels = []int{
			// Thanks to Paul Anderson and Rich T for testing the US frequencies
			902419338, 902921088, 903422839, 903924589, 904426340, 904928090, // US freq per 20190326
			905429841, 905931591, 906433342, 906935092, 907436843, 907938593, 
			908440344, 908942094, 909443845, 909945595, 910447346, 910949096, 
			911450847, 911952597, 912454348, 912956099, 913457849, 913959599, 
			914461350, 914963100, 915464850, 915966601, 916468351, 916970102, 
			917471852, 917973603, 918475353, 918977104, 919478854, 919980605, 
			920482355, 920984106, 921485856, 921987607, 922489357, 922991108, 
			923492858, 923994609, 924496359, 924998110, 925499860, 926001611, 
			926503361, 927005112, 927506862,  
		}
		p.ChannelCount = len(p.channels)
		p.hopIdx = rand.Intn(p.ChannelCount)
		p.hopPattern = []int{
			0, 19, 41, 25, 8, 47, 32, 13, 36, 22, 3, 29, 44, 16, 5, 27, 38, 10,
			49, 21, 2, 30, 42, 14, 48, 7, 24, 34, 45, 1, 17, 39, 26, 9, 31, 50,
			37, 12, 20, 33, 4, 43, 28, 15, 35, 6, 40, 11, 23, 46, 18,
		}
		p.reverseHopPatrn = []int{
			0, 29, 20, 10, 40, 14, 45, 25, 4, 33, 17, 47, 37, 7, 23, 43, 13, 
			30, 50, 1, 38, 19, 9, 48, 26, 3, 32, 15, 42, 11, 21, 34, 6, 39, 
			27, 44, 8, 36, 16, 31, 46, 2, 22, 41, 12, 28, 49, 5, 24, 18, 35, 
		}
	}
	return
}

type Hop struct {
	ChannelIdx  int
	ChannelFreq int
	FreqError   int
}

func (h Hop) String() string {
	return fmt.Sprintf("{ChannelIdx:%d ChannelFreq:%d FreqError:%d}",
		h.ChannelIdx, h.ChannelFreq, h.FreqError,
	)
}

func (p *Parser) hop() (h Hop) {
	h.ChannelIdx = p.hopPattern[p.hopIdx]
	h.ChannelFreq = p.channels[h.ChannelIdx]
	h.FreqError = p.freqError
	return h
}

// Set the pattern index and return the new channel's parameters.
func (p *Parser) SetHop(n int) Hop {
	p.hopIdx = n % p.ChannelCount
	return p.hop()
}

// Find sequence-id with hop-id
func (p *Parser) HopToSeq(n int) int {
	return p.reverseHopPatrn[n % p.ChannelCount]
}

// Find hop-id with sequence-id
func (p *Parser) SeqToHop(n int) int {
	return p.hopPattern[n % p.ChannelCount]
}

// Given a list of packets, check them for validity and ignore duplicates,
// return a list of parsed messages.
func (p *Parser) Parse(pkts []dsp.Packet) (msgs []Message) {
	seen := make(map[string]bool)

	for _, pkt := range pkts {
		// Bit order over-the-air is reversed.
		for idx, b := range pkt.Data {
			pkt.Data[idx] = SwapBitOrder(b)
		}
		// Keep track of duplicate packets.
		s := string(pkt.Data)
		if seen[s] {
			continue
		}
		seen[s] = true

		// If the checksum fails, bail.
		if p.Checksum(pkt.Data[2:]) != 0 {
			continue
		}
		// Look at the packet's tail to determine frequency error between
		// transmitter and receiver.
		lower := pkt.Idx + 8*p.Cfg.SymbolLength
		upper := pkt.Idx + 24*p.Cfg.SymbolLength
		tail := p.Demodulator.Discriminated[lower:upper]
		var mean float64
		for _, sample := range tail {
			mean += sample
		}
		mean /= float64(len(tail))

		// The tail is a series of zero symbols. The driminator's output is
		// measured in radians.
		freqerr := -int(9600 + (mean*float64(p.Cfg.SampleRate))/(2*math.Pi))
		msg := NewMessage(pkt)
		msgs = append(msgs, msg)
		// Per transmitter and per channel we have a list of p.maxTrChList frequency errors
		// The average value of the frequencu erreors in the list is used for the frequency correction.
		tr := int(msg.ID)
		ch := p.hopPattern[p.hopIdx]
//		old := p.freqerrTrChList[tr][ch][p.freqerrTrChPtr[tr][ch]]
		p.freqerrTrChSum[tr][ch] = p.freqerrTrChSum[tr][ch] - p.freqerrTrChList[tr][ch][p.freqerrTrChPtr[tr][ch]] + freqerr
		p.freqerrTrChList[tr][ch][p.freqerrTrChPtr[tr][ch]] = freqerr
		p.freqerrTrChPtr[tr][ch] = (p.freqerrTrChPtr[tr][ch] + 1) % p.maxTrChList
		p.freqError = p.freqerrTrChSum[tr][ch] / 10
//		log.Printf("tr=%d ch=%d old=%d freqerr=%d avgfreqErr=%d ptr=%d sum=%d", tr, ch, old, freqerr, p.freqError, p.freqerrTrChPtr[tr][ch], p.freqerrTrChSum[tr][ch])
//		log.Printf("freqerrTrChList=%d", p.freqerrTrChList[tr][ch])
	}
	return
}

type Message struct {
	dsp.Packet
	ID 	byte
}

func NewMessage(pkt dsp.Packet) (m Message) {
	m.Idx = pkt.Idx
	m.Data = make([]byte, len(pkt.Data)-2)
	copy(m.Data, pkt.Data[2:])
	m.ID = m.Data[0] & 0x7
	return m
}

func (m Message) String() string {
	return fmt.Sprintf("{ID:%d}", m.ID)
}

func SwapBitOrder(b byte) byte {
	b = ((b & 0xF0) >> 4) | ((b & 0x0F) << 4)
	b = ((b & 0xCC) >> 2) | ((b & 0x33) << 2)
	b = ((b & 0xAA) >> 1) | ((b & 0x55) << 1)
	return b
}
