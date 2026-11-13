//go:build linux && (!ffmpeg_static || !cgo || purego)

package fb

const recordingTestsUseExternalEncoder = true
