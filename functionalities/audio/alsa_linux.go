//go:build linux

package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"github.com/hajimehoshi/go-mp3"
	"golang.org/x/sys/unix"
)

const (
	maxALSACards         = 32
	maxMixerControls     = 1024
	maxControlValues     = 128
	maxPCMBufferFrames   = 65536
	pcmPollInterval      = 50 * time.Millisecond
	minimumDrainDeadline = 500 * time.Millisecond
	maximumDrainDeadline = 3 * time.Second
)

type alsaPlayer struct {
	file         *os.File
	channels     int
	rate         uint32
	bufferFrames uint32
}

func alsaIOCTL(fd int, request uintptr, value unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, uintptr(value))
	runtime.KeepAlive(value)
	if errno != 0 {
		return errno
	}
	return nil
}

func initHWParams() pcmHWParams {
	var params pcmHWParams
	for i := range params.Masks {
		for word := range params.Masks[i].Bits {
			params.Masks[i].Bits[word] = ^uint32(0)
		}
	}
	for i := range params.Intervals {
		params.Intervals[i].Max = ^uint32(0)
	}
	params.RMask = ^uint32(0)
	params.Info = ^uint32(0)
	return params
}

func hwInterval(params *pcmHWParams, parameter int) *pcmInterval {
	return &params.Intervals[parameter-pcmFirstInterval]
}

func markHWParameter(params *pcmHWParams, parameter int) {
	params.RMask |= 1 << parameter
	params.CMask |= 1 << parameter
}

func setMaskValue(params *pcmHWParams, parameter int, value uint32) {
	mask := &params.Masks[parameter]
	for i := range mask.Bits {
		mask.Bits[i] = 0
	}
	mask.Bits[value/32] = 1 << (value % 32)
	markHWParameter(params, parameter)
}

func setIntervalValue(params *pcmHWParams, parameter int, value uint32) {
	interval := hwInterval(params, parameter)
	interval.Min = value
	interval.Max = value
	interval.Flags = intervalInteger
	markHWParameter(params, parameter)
}

func refineHWParams(fd int, params *pcmHWParams) error {
	return alsaIOCTL(fd, pcmIOCTLHWRefine, unsafe.Pointer(params))
}

func usableInterval(interval *pcmInterval) (minimum, maximum uint32, ok bool) {
	if interval.Flags&intervalEmpty != 0 {
		return 0, 0, false
	}
	minimum, maximum = interval.Min, interval.Max
	if interval.Flags&intervalOpenMin != 0 {
		if minimum == ^uint32(0) {
			return 0, 0, false
		}
		minimum++
	}
	if interval.Flags&intervalOpenMax != 0 {
		if maximum == 0 {
			return 0, 0, false
		}
		maximum--
	}
	return minimum, maximum, minimum <= maximum
}

func exactIntervalValue(params *pcmHWParams, parameter int) (uint32, bool) {
	minimum, maximum, ok := usableInterval(hwInterval(params, parameter))
	return minimum, ok && minimum == maximum
}

func constrainRateMaximum(params *pcmHWParams, value uint32) bool {
	interval := hwInterval(params, pcmParamRate)
	minimum, maximum, ok := usableInterval(interval)
	if !ok || value < minimum {
		return false
	}
	if value < maximum {
		maximum = value
	}
	interval.Min, interval.Max = minimum, maximum
	interval.Flags = intervalInteger
	markHWParameter(params, pcmParamRate)
	return true
}

func constrainRateMinimum(params *pcmHWParams, value uint32) bool {
	interval := hwInterval(params, pcmParamRate)
	minimum, maximum, ok := usableInterval(interval)
	if !ok || value > maximum {
		return false
	}
	if value > minimum {
		minimum = value
	}
	interval.Min, interval.Max = minimum, maximum
	interval.Flags = intervalInteger
	markHWParameter(params, pcmParamRate)
	return true
}

func fixedRateCandidate(fd int, base pcmHWParams, requested uint32, lower bool) (pcmHWParams, uint32, bool) {
	candidate := base
	var constrained bool
	if lower {
		constrained = constrainRateMaximum(&candidate, requested)
	} else {
		constrained = constrainRateMinimum(&candidate, requested)
	}
	if !constrained || refineHWParams(fd, &candidate) != nil {
		return pcmHWParams{}, 0, false
	}
	minimum, maximum, ok := usableInterval(hwInterval(&candidate, pcmParamRate))
	if !ok {
		return pcmHWParams{}, 0, false
	}
	value := minimum
	if lower {
		value = maximum
	}
	setIntervalValue(&candidate, pcmParamRate, value)
	if refineHWParams(fd, &candidate) != nil {
		return pcmHWParams{}, 0, false
	}
	actual, exact := exactIntervalValue(&candidate, pcmParamRate)
	return candidate, actual, exact
}

func setRateNear(fd int, params *pcmHWParams, requested uint32) error {
	exact := *params
	setIntervalValue(&exact, pcmParamRate, requested)
	if refineHWParams(fd, &exact) == nil {
		if _, ok := exactIntervalValue(&exact, pcmParamRate); ok {
			*params = exact
			return nil
		}
	}
	lowerParams, lowerRate, hasLower := fixedRateCandidate(fd, *params, requested, true)
	upperParams, upperRate, hasUpper := fixedRateCandidate(fd, *params, requested, false)
	if !hasLower && !hasUpper {
		return fmt.Errorf("no supported sample rate near %d", requested)
	}
	if hasLower && (!hasUpper || uint64(requested)-uint64(lowerRate) <= uint64(upperRate)-uint64(requested)) {
		*params = lowerParams
	} else {
		*params = upperParams
	}
	return nil
}

func appendUnique(values []uint32, value uint32) []uint32 {
	if value == 0 {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func configurePeriods(fd int, params *pcmHWParams) error {
	sizes := []uint32{1024, 512, 2048, 256, 4096}
	counts := []uint32{4, 3, 2}
	if minimum, maximum, ok := usableInterval(hwInterval(params, pcmParamPeriodSize)); ok {
		sizes = appendUnique(sizes, minimum)
		if maximum <= 4096 {
			sizes = appendUnique(sizes, maximum)
		}
	}
	if minimum, maximum, ok := usableInterval(hwInterval(params, pcmParamPeriods)); ok {
		counts = appendUnique(counts, minimum)
		if maximum <= 32 {
			counts = appendUnique(counts, maximum)
		}
	}
	for _, size := range sizes {
		for _, count := range counts {
			buffer := uint64(size) * uint64(count)
			if buffer == 0 || buffer > maxPCMBufferFrames {
				continue
			}
			candidate := *params
			setIntervalValue(&candidate, pcmParamPeriodSize, size)
			if refineHWParams(fd, &candidate) != nil {
				continue
			}
			setIntervalValue(&candidate, pcmParamPeriods, count)
			if refineHWParams(fd, &candidate) != nil {
				continue
			}
			setIntervalValue(&candidate, pcmParamBufferSize, uint32(buffer))
			if refineHWParams(fd, &candidate) != nil {
				continue
			}
			*params = candidate
			return nil
		}
	}
	return fmt.Errorf("no bounded PCM period configuration is supported")
}

func configurePCM(fd int, sampleRate uint32, channels int) (actualRate, bufferFrames uint32, err error) {
	params := initHWParams()
	if err := refineHWParams(fd, &params); err != nil {
		return 0, 0, fmt.Errorf("query hardware parameters: %w", err)
	}
	setMaskValue(&params, pcmParamAccess, pcmAccessRWInterleave)
	setMaskValue(&params, pcmParamFormat, pcmFormatS16LE)
	setMaskValue(&params, pcmParamSubformat, 0)
	setIntervalValue(&params, pcmParamChannels, uint32(channels))
	if err := refineHWParams(fd, &params); err != nil {
		return 0, 0, fmt.Errorf("configure 16-bit interleaved stereo: %w", err)
	}
	if err := setRateNear(fd, &params, sampleRate); err != nil {
		return 0, 0, err
	}
	if err := configurePeriods(fd, &params); err != nil {
		return 0, 0, err
	}
	if err := alsaIOCTL(fd, pcmIOCTLHWParams, unsafe.Pointer(&params)); err != nil {
		return 0, 0, fmt.Errorf("apply hardware parameters: %w", err)
	}
	actualRate, rateOK := exactIntervalValue(&params, pcmParamRate)
	periodSize, periodOK := exactIntervalValue(&params, pcmParamPeriodSize)
	bufferFrames, bufferOK := exactIntervalValue(&params, pcmParamBufferSize)
	if !rateOK || !periodOK || !bufferOK || actualRate == 0 || periodSize == 0 || bufferFrames == 0 || bufferFrames > maxPCMBufferFrames {
		return 0, 0, fmt.Errorf("driver returned invalid PCM parameters")
	}
	boundary := alsaULong(bufferFrames)
	for boundary <= alsaULong(maxALSALong)/2-alsaULong(bufferFrames) {
		boundary *= 2
	}
	sw := pcmSWParams{
		PeriodStep:     1,
		AvailMin:       alsaULong(periodSize),
		TransferAlign:  1,
		StartThreshold: 1,
		StopThreshold:  alsaULong(bufferFrames),
		Boundary:       boundary,
		Protocol:       2<<16 | 18,
	}
	if err := alsaIOCTL(fd, pcmIOCTLSWParams, unsafe.Pointer(&sw)); err != nil {
		return 0, 0, fmt.Errorf("apply software parameters: %w", err)
	}
	if err := alsaIOCTL(fd, pcmIOCTLPrepare, nil); err != nil {
		return 0, 0, fmt.Errorf("prepare PCM: %w", err)
	}
	return actualRate, bufferFrames, nil
}

func openALSA(sampleRate int) (*alsaPlayer, error) {
	if sampleRate <= 0 || uint64(sampleRate) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("invalid sample rate %d", sampleRate)
	}
	var lastError error
	for card := 0; card < maxALSACards; card++ {
		path := fmt.Sprintf("/dev/snd/pcmC%dD0p", card)
		file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				lastError = err
			}
			continue
		}
		actualRate, bufferFrames, err := configurePCM(int(file.Fd()), uint32(sampleRate), 2)
		if err == nil {
			return &alsaPlayer{file: file, channels: 2, rate: actualRate, bufferFrames: bufferFrames}, nil
		}
		lastError = err
		_ = file.Close()
	}
	if lastError != nil {
		alsaEmitError(lastError.Error())
	}
	return nil, fmt.Errorf("cannot open the ALSA playback device")
}

func pollPCM(ctx context.Context, fd int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
	for {
		_, err := unix.Poll(poll, int(pcmPollInterval/time.Millisecond))
		if errors.Is(err, syscall.EINTR) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			continue
		}
		if err != nil {
			return err
		}
		if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return fmt.Errorf("ALSA device became unavailable")
		}
		return ctx.Err()
	}
}

func (p *alsaPlayer) sendContext(ctx context.Context, data []byte) error {
	if p == nil || p.file == nil || len(data) == 0 {
		return nil
	}
	frameBytes := 2 * p.channels
	frames := len(data) / frameBytes
	if frames == 0 {
		return nil
	}
	data = data[:frames*frameBytes]
	written := 0
	for written < frames {
		if err := ctx.Err(); err != nil {
			return err
		}
		xfer := pcmXfer{
			Buffer: uintptr(unsafe.Pointer(&data[written*frameBytes])),
			Frames: alsaULong(frames - written),
		}
		err := alsaIOCTL(int(p.file.Fd()), pcmIOCTLWrite, unsafe.Pointer(&xfer))
		runtime.KeepAlive(data)
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
			if errors.Is(err, syscall.EAGAIN) {
				if err := pollPCM(ctx, int(p.file.Fd())); err != nil {
					return err
				}
			}
			continue
		}
		if errors.Is(err, syscall.EPIPE) {
			if prepareErr := alsaIOCTL(int(p.file.Fd()), pcmIOCTLPrepare, nil); prepareErr != nil {
				return fmt.Errorf("recover ALSA underrun: %w", prepareErr)
			}
			return nil // Match the former wrapper: drop this chunk after recovery.
		}
		if err != nil {
			return fmt.Errorf("ALSA write failed: %w", err)
		}
		if xfer.Result <= 0 || uint64(xfer.Result) > uint64(frames-written) {
			return fmt.Errorf("ALSA write returned an invalid frame count")
		}
		written += int(xfer.Result)
	}
	return nil
}

func (p *alsaPlayer) send(data []byte) error {
	return p.sendContext(context.Background(), data)
}

func (p *alsaPlayer) drain() {
	if p == nil || p.file == nil {
		return
	}
	duration := minimumDrainDeadline
	if p.rate != 0 && p.bufferFrames != 0 {
		duration += time.Duration(uint64(p.bufferFrames) * uint64(time.Second) / uint64(p.rate))
	}
	if duration > maximumDrainDeadline {
		duration = maximumDrainDeadline
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	for {
		err := alsaIOCTL(int(p.file.Fd()), pcmIOCTLDrain, nil)
		if err == nil {
			return
		}
		if !errors.Is(err, syscall.EAGAIN) {
			return
		}
		if pollPCM(ctx, int(p.file.Fd())) != nil {
			return
		}
	}
}

func (p *alsaPlayer) closeDevice(drain bool) {
	if p == nil || p.file == nil {
		return
	}
	if drain {
		p.drain()
	} else {
		_ = alsaIOCTL(int(p.file.Fd()), pcmIOCTLDrop, nil)
	}
	_ = p.file.Close()
	p.file = nil
}

func (p *alsaPlayer) close() { p.closeDevice(true) }

// Preserve the original go-mp3 -> 16-bit stereo -> direct-hardware PCM chain.
// No subprocess, global mixer write, device probing for video, or attenuation.
func playMP3(ctx context.Context, path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("audio requires a regular media file")
	}
	decoder, err := mp3.NewDecoder(f)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	player, err := openALSA(decoder.SampleRate())
	if err != nil {
		return err
	}
	finished := false
	defer func() { player.closeDevice(finished) }()
	buffer := make([]byte, 8192)
	for ctx.Err() == nil {
		n, readErr := decoder.Read(buffer)
		if n > 0 {
			if err := player.sendContext(ctx, buffer[:n]); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
		if readErr == io.EOF {
			finished = true
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

type mixerControl struct {
	file *os.File
	info ctlElementInfo
}

type outputMixer struct {
	file            *os.File
	headphoneSwitch *mixerControl
	speakerSwitch   *mixerControl
	controls        map[string]ctlElementID
}

func cString(value []byte) string {
	for i, b := range value {
		if b == 0 {
			return string(value[:i])
		}
	}
	return string(value)
}

func discoveredOutputCard() int {
	highest := -1
	found := 0
	for card := 0; card < maxALSACards && found < 2; card++ {
		if _, err := os.Stat(fmt.Sprintf("/dev/snd/controlC%d", card)); err == nil {
			highest = card
			found++
		}
	}
	return highest
}

func readControlIDs(file *os.File) (map[string]ctlElementID, error) {
	var list ctlElementList
	if err := alsaIOCTL(int(file.Fd()), ctlIOCTLElementList, unsafe.Pointer(&list)); err != nil {
		return nil, err
	}
	if list.Count == 0 || list.Count > maxMixerControls {
		return nil, fmt.Errorf("invalid ALSA control count %d", list.Count)
	}
	ids := make([]ctlElementID, list.Count)
	list.Space = list.Count
	list.IDs = uintptr(unsafe.Pointer(&ids[0]))
	if err := alsaIOCTL(int(file.Fd()), ctlIOCTLElementList, unsafe.Pointer(&list)); err != nil {
		return nil, err
	}
	runtime.KeepAlive(ids)
	if list.Used > list.Space {
		return nil, fmt.Errorf("invalid ALSA control response")
	}
	controls := make(map[string]ctlElementID, list.Used)
	for _, id := range ids[:list.Used] {
		if id.Interface == ctlInterfaceMixer && id.Index == 0 {
			controls[cString(id.Name[:])] = id
		}
	}
	return controls, nil
}

func findControl(file *os.File, controls map[string]ctlElementID, kind int32, names ...string) (*mixerControl, error) {
	for _, name := range names {
		id, ok := controls[name]
		if !ok {
			continue
		}
		info := ctlElementInfo{ID: id}
		if err := alsaIOCTL(int(file.Fd()), ctlIOCTLElementInfo, unsafe.Pointer(&info)); err != nil {
			continue
		}
		if info.Type != kind || info.Count == 0 || info.Count > maxControlValues {
			continue
		}
		return &mixerControl{file: file, info: info}, nil
	}
	return nil, fmt.Errorf("ALSA control not found")
}

func openOutputMixer(requestedCard int) (*outputMixer, error) {
	card := requestedCard
	if card < 0 {
		card = discoveredOutputCard()
	}
	if card < 0 || card >= maxALSACards {
		return nil, fmt.Errorf("no ALSA output card")
	}
	file, err := os.OpenFile(fmt.Sprintf("/dev/snd/controlC%d", card), os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	controls, err := readControlIDs(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	headphone, err := findControl(file, controls, ctlElementBoolean, "Headphone Playback Switch", "Headphone Switch", "Headphone")
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	speaker, err := findControl(file, controls, ctlElementBoolean, "Speaker Playback Switch", "Speaker Switch", "Speaker")
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &outputMixer{file: file, headphoneSwitch: headphone, speakerSwitch: speaker, controls: controls}, nil
}

func (m *outputMixer) close() {
	if m != nil && m.file != nil {
		_ = m.file.Close()
		m.file = nil
	}
}

func (control *mixerControl) readValues() (*ctlElementValue, []alsaLong, error) {
	if control == nil || control.file == nil || control.info.Count == 0 || control.info.Count > maxControlValues || control.info.Access&ctlAccessRead == 0 {
		return nil, nil, fmt.Errorf("invalid or unreadable ALSA control")
	}
	value := &ctlElementValue{ID: control.info.ID}
	if err := alsaIOCTL(int(control.file.Fd()), ctlIOCTLElementRead, unsafe.Pointer(value)); err != nil {
		return nil, nil, err
	}
	return value, controlLongs(value, int(control.info.Count)), nil
}

func (control *mixerControl) enabled() (bool, error) {
	_, values, err := control.readValues()
	if err != nil {
		return false, err
	}
	return values[0] != 0, nil
}

func (control *mixerControl) setAll(value alsaLong) error {
	if control.info.Access&ctlAccessWrite == 0 {
		return fmt.Errorf("ALSA control is read-only")
	}
	element, values, err := control.readValues()
	if err != nil {
		return err
	}
	for i := range values {
		values[i] = value
	}
	return alsaIOCTL(int(control.file.Fd()), ctlIOCTLElementWrite, unsafe.Pointer(element))
}

func (m *outputMixer) selected() (int, bool) {
	headphone, hpErr := m.headphoneSwitch.enabled()
	speaker, spErr := m.speakerSwitch.enabled()
	if hpErr != nil || spErr != nil {
		return 0, false
	}
	if headphone {
		return 1, true
	}
	if speaker {
		return 0, true
	}
	return 0, false
}

// SelectedOutput reads the hardware route without opening or changing PCM.
// Mode 0 is the speaker and mode 1 the earphone outlet.
func SelectedOutput() (mode int, known bool) {
	card, ok := outputCard()
	if !ok {
		return 0, false
	}
	mixer, err := openOutputMixer(card)
	if err != nil {
		return 0, false
	}
	defer mixer.close()
	return mixer.selected()
}

// SetOutput mutes the old route before enabling speaker (0) or earphone (1).
func SetOutput(mode int) bool {
	if mode != 0 && mode != 1 {
		return false
	}
	card, ok := outputCard()
	if !ok {
		return false
	}
	mixer, err := openOutputMixer(card)
	if err != nil {
		return false
	}
	defer mixer.close()
	off, on := mixer.headphoneSwitch, mixer.speakerSwitch
	if mode == 1 {
		off, on = mixer.speakerSwitch, mixer.headphoneSwitch
	}
	return off.setAll(0) == nil && on.setAll(1) == nil
}

func controlPercentValue(minimum, maximum alsaLong, percent int) (alsaLong, bool) {
	min64, max64 := int64(minimum), int64(maximum)
	if percent < 0 || percent > 100 || max64 <= min64 || min64 < -(1<<60) || max64 > 1<<60 {
		return 0, false
	}
	delta := max64 - min64
	scaled := (delta/100)*int64(percent) + (delta%100)*int64(percent)/100
	return alsaLong(min64 + scaled), true
}

func (m *outputMixer) volumeControl(mode int) (*mixerControl, error) {
	base := "Speaker"
	if mode == 1 {
		base = "Headphone"
	}
	return findControl(m.file, m.controls, ctlElementInteger, base+" Playback Volume", base+" Volume", base)
}

// SetVolume adjusts every channel of the currently selected hardware route.
func SetVolume(percent int) bool {
	if percent < 0 || percent > 100 {
		return false
	}
	card, ok := outputCard()
	if !ok {
		return false
	}
	mixer, err := openOutputMixer(card)
	if err != nil {
		return false
	}
	defer mixer.close()
	mode, known := mixer.selected()
	if !known {
		return false
	}
	control, err := mixer.volumeControl(mode)
	if err != nil {
		return false
	}
	rangeValues := controlInfoLongs(&control.info)
	value, ok := controlPercentValue(rangeValues[0], rangeValues[1], percent)
	return ok && control.setAll(value) == nil
}

func outputCard() (card int, ok bool) {
	card = -1
	if value := os.Getenv("ST_GO_AUDIO_OUTPUT_CARD"); value != "" {
		var err error
		card, err = strconv.Atoi(value)
		if err != nil || card < 0 || card >= maxALSACards {
			return 0, false
		}
	}
	return card, true
}
