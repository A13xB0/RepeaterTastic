// Package phy derives LoRa parameters the way Meshtastic does from region, preset and primary channel.
//
// Mirrors firmware src/mesh/RadioInterface.cpp (regions, applyModemConfig, computeSlotTimeMsec,
// getTxDelayMsecWeighted, getRetransmissionMsec) and src/mesh/MeshRadio.h (modemPresetToParams) at 2.8.1.
package phy

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"

	"github.com/A13xB0/RepeaterTastic/internal/pb"
)

const (
	SyncWord        = 0x2B
	PreambleSymbols = 16
	CWMin           = 3
	CWMax           = 8
	ProcessingMs    = 4500
)

type PresetParams struct {
	BwKHz   float64
	SF      int
	CR      int // coding rate denominator 5..8
	Display string
}

type Preset = pb.Config_LoRaConfig_ModemPreset

var Presets = map[Preset]PresetParams{
	pb.Config_LoRaConfig_SHORT_TURBO:   {500, 7, 5, "ShortTurbo"},
	pb.Config_LoRaConfig_SHORT_FAST:    {250, 7, 5, "ShortFast"},
	pb.Config_LoRaConfig_SHORT_SLOW:    {250, 8, 5, "ShortSlow"},
	pb.Config_LoRaConfig_MEDIUM_FAST:   {250, 9, 5, "MediumFast"},
	pb.Config_LoRaConfig_MEDIUM_SLOW:   {250, 10, 5, "MediumSlow"},
	pb.Config_LoRaConfig_MEDIUM_TURBO:  {500, 9, 5, "MediumTurbo"},
	pb.Config_LoRaConfig_LONG_FAST:     {250, 11, 5, "LongFast"},
	pb.Config_LoRaConfig_LONG_MODERATE: {125, 11, 8, "LongMod"},
	pb.Config_LoRaConfig_LONG_SLOW:     {125, 12, 8, "LongSlow"},
	pb.Config_LoRaConfig_LONG_TURBO:    {500, 11, 8, "LongTurbo"},
	pb.Config_LoRaConfig_LITE_FAST:     {125, 9, 5, "LiteFast"},
	pb.Config_LoRaConfig_LITE_SLOW:     {125, 10, 5, "LiteSlow"},
	pb.Config_LoRaConfig_NARROW_FAST:   {62.5, 7, 6, "NarrowFast"},
	pb.Config_LoRaConfig_NARROW_SLOW:   {62.5, 8, 6, "NarrowSlow"},
	pb.Config_LoRaConfig_TINY_FAST:     {15.6, 7, 5, "TinyFast"},
	pb.Config_LoRaConfig_TINY_SLOW:     {15.6, 8, 6, "TinySlow"},
}

type RegionInfo struct {
	Code          pb.Config_LoRaConfig_RegionCode
	Name          string
	StartMHz      float64
	EndMHz        float64
	DutyCyclePct  float64
	PowerLimitDBm int
	SpacingMHz    float64
	PaddingMHz    float64
	OverrideSlot  int // >0 explicit 1-based, -1 preset-name hash, 0 channel-name hash
	WideLoRa      bool
}

func r(code pb.Config_LoRaConfig_RegionCode, start, end, duty float64, power int) RegionInfo {
	return RegionInfo{Code: code, Name: code.String(), StartMHz: start, EndMHz: end, DutyCyclePct: duty, PowerLimitDBm: power}
}

var Regions = func() map[string]RegionInfo {
	list := []RegionInfo{
		r(pb.Config_LoRaConfig_US, 902.0, 928.0, 100, 30),
		r(pb.Config_LoRaConfig_EU_433, 433.0, 434.0, 10, 10),
		r(pb.Config_LoRaConfig_EU_868, 869.4, 869.65, 10, 27),
		r(pb.Config_LoRaConfig_CN, 470.0, 510.0, 100, 19),
		r(pb.Config_LoRaConfig_JP, 920.5, 923.5, 100, 13),
		r(pb.Config_LoRaConfig_ANZ, 915.0, 928.0, 100, 30),
		r(pb.Config_LoRaConfig_ANZ_433, 433.05, 434.79, 100, 14),
		r(pb.Config_LoRaConfig_RU, 868.7, 869.2, 100, 20),
		r(pb.Config_LoRaConfig_KR, 920.0, 923.0, 100, 23),
		r(pb.Config_LoRaConfig_TW, 920.0, 925.0, 100, 27),
		r(pb.Config_LoRaConfig_IN, 865.0, 867.0, 100, 30),
		r(pb.Config_LoRaConfig_NZ_865, 864.0, 868.0, 100, 36),
		r(pb.Config_LoRaConfig_TH, 920.0, 925.0, 10, 27),
		r(pb.Config_LoRaConfig_UA_433, 433.0, 434.7, 10, 10),
		r(pb.Config_LoRaConfig_MY_433, 433.0, 435.0, 100, 20),
		r(pb.Config_LoRaConfig_MY_919, 919.0, 924.0, 100, 27),
		r(pb.Config_LoRaConfig_SG_923, 917.0, 925.0, 100, 20),
		r(pb.Config_LoRaConfig_PH_433, 433.0, 434.7, 100, 10),
		r(pb.Config_LoRaConfig_PH_868, 868.0, 869.4, 100, 14),
		r(pb.Config_LoRaConfig_PH_915, 915.0, 918.0, 100, 24),
		r(pb.Config_LoRaConfig_KZ_433, 433.075, 434.775, 100, 10),
		r(pb.Config_LoRaConfig_KZ_863, 863.0, 868.0, 100, 30),
		r(pb.Config_LoRaConfig_NP_865, 865.0, 868.0, 100, 30),
		r(pb.Config_LoRaConfig_BR_902, 902.0, 907.5, 100, 30),
	}
	eu866 := r(pb.Config_LoRaConfig_EU_866, 865.6, 867.6, 2.5, 27)
	eu866.SpacingMHz, eu866.PaddingMHz = 0.4, 0.0375
	eun868 := r(pb.Config_LoRaConfig_EU_N_868, 869.4, 869.65, 10, 27)
	eun868.PaddingMHz, eun868.OverrideSlot = 0.0104, 1
	l24 := r(pb.Config_LoRaConfig_LORA_24, 2400.0, 2483.5, 100, 10)
	l24.WideLoRa = true
	list = append(list, eu866, eun868, l24)
	m := make(map[string]RegionInfo, len(list))
	for _, ri := range list {
		m[ri.Name] = ri
	}
	return m
}()

// DJB2 is the firmware's hash() over the primary channel name.
func DJB2(s string) uint32 {
	h := uint32(5381)
	for i := 0; i < len(s); i++ {
		h = h*33 + uint32(s[i])
	}
	return h
}

// RadioParams is everything the modem and the scheduler need.
type RadioParams struct {
	Region       RegionInfo
	Preset       Preset
	FrequencyMHz float64
	BwKHz        float64
	SF           int
	CR           int
	Slot         int // 0-based, -1 when frequency is overridden
	NumSlots     int
	Preamble     int
	SyncWord     uint8
	TxPowerDBm   int
}

func (p RadioParams) FrequencyHz() uint32 { return uint32(math.Round(p.FrequencyMHz * 1e6)) }
func (p RadioParams) BwHz() uint32        { return uint32(math.Round(p.BwKHz * 1000)) }
func (p RadioParams) PresetName() string  { return Presets[p.Preset].Display }
func (p RadioParams) SlotTimeMs() float64 { return SlotTimeMs(p.SF, p.BwKHz) }
func (p RadioParams) AirtimeMs(frameLen int) float64 {
	return AirtimeMs(frameLen, p.SF, p.BwKHz, p.CR, p.Preamble)
}

type Options struct {
	Region             string
	Preset             Preset
	PrimaryChannelName string
	ChannelNum         int     // 1-based user override, 0 = hash
	OverrideFreqMHz    float64 // 0 = derive
	FreqOffsetMHz      float64
	TxPowerDBm         int
}

// Resolve computes the radio parameters (applyModemConfig).
func Resolve(o Options) (RadioParams, error) {
	reg, ok := Regions[strings.ToUpper(o.Region)]
	if !ok {
		return RadioParams{}, fmt.Errorf("unknown region %q", o.Region)
	}
	pp, ok := Presets[o.Preset]
	if !ok {
		return RadioParams{}, fmt.Errorf("unknown preset %v", o.Preset)
	}
	// float32 like the firmware, so rounding at slot boundaries matches.
	slotWidth := float32(reg.SpacingMHz) + 2*float32(reg.PaddingMHz) + float32(pp.BwKHz)/1000
	numSlots := int(math.Round(float64((float32(reg.EndMHz) - float32(reg.StartMHz) + float32(reg.SpacingMHz)) / slotWidth)))
	name := o.PrimaryChannelName
	if name == "" {
		name = pp.Display
	}
	rp := RadioParams{Region: reg, Preset: o.Preset, BwKHz: pp.BwKHz, SF: pp.SF, CR: pp.CR, NumSlots: numSlots,
		Preamble: PreambleSymbols, SyncWord: SyncWord}
	if reg.WideLoRa {
		rp.Preamble = 12
	}
	if o.OverrideFreqMHz != 0 {
		rp.Slot = -1
		rp.FrequencyMHz = o.OverrideFreqMHz
	} else {
		switch {
		case o.ChannelNum > 0:
			rp.Slot = o.ChannelNum - 1
		case reg.OverrideSlot > 0:
			rp.Slot = reg.OverrideSlot - 1
		case numSlots == 0:
			rp.Slot = 0
		case reg.OverrideSlot == -1:
			rp.Slot = int(DJB2(pp.Display) % uint32(numSlots))
		default:
			rp.Slot = int(DJB2(name) % uint32(numSlots))
		}
		f := float32(reg.StartMHz) + float32(pp.BwKHz)/2000 + float32(reg.PaddingMHz) + float32(rp.Slot)*slotWidth
		rp.FrequencyMHz = math.Round(float64(f)*1e4) / 1e4
	}
	rp.FrequencyMHz += o.FreqOffsetMHz
	rp.TxPowerDBm = o.TxPowerDBm
	if rp.TxPowerDBm <= 0 || rp.TxPowerDBm > reg.PowerLimitDBm {
		rp.TxPowerDBm = reg.PowerLimitDBm
	}
	return rp, nil
}

// SlotTimeMs is computeSlotTimeMsec for sub-GHz SX126x/SX127x.
func SlotTimeMs(sf int, bwKHz float64) float64 {
	sym := float64(int(1)<<sf) / bwKHz
	return math.Max(2.25, 2+0.5)*sym + (0.2 + 0.4 + 7)
}

// AirtimeMs is Semtech's LoRa time-on-air (explicit header, CRC on).
func AirtimeMs(payloadLen, sf int, bwKHz float64, cr, preamble int) float64 {
	tSym := float64(int(1)<<sf) / bwKHz
	de := 0
	if tSym >= 16 {
		de = 1
	}
	var nPre float64
	var num int
	if sf == 5 || sf == 6 {
		nPre = float64(preamble) + 6.25
		num = 8*payloadLen + 16 - 4*sf
	} else {
		nPre = float64(preamble) + 4.25
		num = 8*payloadLen + 16 - 4*sf + 28
	}
	den := 4 * (sf - 2*de)
	nPayload := 8 + max(int(math.Ceil(float64(num)/float64(den)))*cr, 0)
	return (nPre + float64(nPayload)) * tSym
}

func arduinoMap(x float64, inMin, inMax, outMin, outMax int) int {
	xi := int(x)
	return (xi-inMin)*(outMax-outMin)/(inMax-inMin) + outMin
}

// CWSizeForSNR is getCWsize.
func CWSizeForSNR(snr float32) int { return arduinoMap(float64(snr), -20, 10, CWMin, CWMax) }

// FloodDelayMs is getTxDelayMsecWeighted: higher SNR waits longer, ROUTER goes early.
func FloodDelayMs(snr float32, slotMs float64, router bool) float64 {
	cw := CWSizeForSNR(snr)
	if router {
		return float64(rand.IntN(max(1, 2*cw))) * slotMs
	}
	return 2*CWMax*slotMs + float64(rand.IntN(1<<cw))*slotMs
}

// OwnTxDelayMs is getTxDelayMsec for packets we originate.
func OwnTxDelayMs(chanUtilPct float64, slotMs float64) float64 {
	cw := arduinoMap(chanUtilPct, 0, 100, CWMin, CWMax)
	return float64(rand.IntN(1<<cw)) * slotMs
}

// RetransmissionMs is getRetransmissionMsec.
func RetransmissionMs(frameLen int, rp RadioParams, chanUtilPct float64) float64 {
	air := rp.AirtimeMs(frameLen)
	cw := arduinoMap(chanUtilPct, 0, 100, CWMin, CWMax)
	return 2*air + float64(int(1)<<cw+2*CWMax+int(1)<<((CWMax+CWMin)/2))*rp.SlotTimeMs() + ProcessingMs
}
