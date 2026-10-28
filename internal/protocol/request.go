// Package protocol is plymouthd's socket protocol: the requests systemd,
// its password agents and the plymouth client send, and the responses the
// daemon answers with, encoded and decoded over a byte stream.
//
// A request is one command byte followed by either a NUL (no argument) or
// 0x02, a length byte counting the argument and its trailing NUL, and the
// NUL-terminated argument. A response is one type byte; ANSWER and
// MULTIPLE_ANSWERS follow it with a little-endian uint32 length and the bytes.
package protocol

import (
	"strconv"
)

// SocketName is the abstract Unix socket plymouthd listens on, as Go's net
// package spells an abstract address.
const SocketName = "@/org/freedesktop/plymouthd"

// Command is a request's command byte, as ply-boot-protocol.h defines them.
type Command byte

// The request commands.
const (
	CmdPing              Command = 'P'
	CmdUpdate            Command = 'U'
	CmdChangeMode        Command = 'C'
	CmdSystemUpdate      Command = 'u'
	CmdSystemInitialized Command = 'S'
	CmdDeactivate        Command = 'D'
	CmdReactivate        Command = 'r'
	CmdQuit              Command = 'Q'
	CmdReload            Command = 'l'
	CmdPassword          Command = '*'
	CmdCachedPassword    Command = 'c'
	CmdQuestion          Command = 'W'
	CmdShowMessage       Command = 'M'
	CmdHideMessage       Command = 'm'
	CmdWatchKeystroke    Command = 'K'
	CmdIgnoreKeystroke   Command = 'L'
	CmdPauseProgress     Command = 'A'
	CmdResumeProgress    Command = 'a'
	CmdShowSplash        Command = '$'
	CmdHideSplash        Command = 'H'
	CmdNewRoot           Command = 'R'
	CmdHasActiveVT       Command = 'V'
	CmdError             Command = '!'
)

// Request is one decoded request, one of the types in this file.
type Request interface {
	// Command is the request's command byte.
	Command() Command
	argument() (string, bool)
}

// Ping asks whether the daemon is running.
type Ping struct{}

// Update reports a status, the name of each unit systemd starts.
type Update struct{ Status string }

// ChangeMode switches the splash's mode, such as "updates" or "shutdown".
type ChangeMode struct{ Mode string }

// SystemUpdate reports an offline update's progress.
type SystemUpdate struct{ Progress Percent }

// SystemInitialized reports that the root file system is writable.
type SystemInitialized struct{}

// Deactivate stops drawing and gives up the display, leaving the frame.
type Deactivate struct{}

// Reactivate takes the display back after Deactivate.
type Reactivate struct{}

// Quit ends the daemon; Retain keeps its last frame on screen.
type Quit struct{ Retain bool }

// Reload reloads the splash.
type Reload struct{}

// Password asks for a secret with Prompt, answered later.
type Password struct{ Prompt string }

// CachedPassword asks for every password typed so far.
type CachedPassword struct{}

// Question asks a question with Prompt whose answer is echoed, answered later.
type Question struct{ Prompt string }

// ShowMessage shows Text on the splash.
type ShowMessage struct{ Text string }

// HideMessage hides Text from the splash.
type HideMessage struct{ Text string }

// WatchKeystroke waits for one of Keys, or any key when Keys is empty.
type WatchKeystroke struct{ Keys string }

// IgnoreKeystroke stops waiting for Keys.
type IgnoreKeystroke struct{ Keys string }

// PauseProgress stops the progress animation.
type PauseProgress struct{}

// ResumeProgress restarts the progress animation.
type ResumeProgress struct{}

// ShowSplash shows the splash.
type ShowSplash struct{}

// HideSplash hides the splash.
type HideSplash struct{}

// NewRoot announces the root the boot is about to switch to.
type NewRoot struct{ Dir string }

// HasActiveVT asks whether the kernel has virtual terminals.
type HasActiveVT struct{}

// Error reports that the boot hit an error.
type Error struct{}

func (Ping) Command() Command              { return CmdPing }
func (Update) Command() Command            { return CmdUpdate }
func (ChangeMode) Command() Command        { return CmdChangeMode }
func (SystemUpdate) Command() Command      { return CmdSystemUpdate }
func (SystemInitialized) Command() Command { return CmdSystemInitialized }
func (Deactivate) Command() Command        { return CmdDeactivate }
func (Reactivate) Command() Command        { return CmdReactivate }
func (Quit) Command() Command              { return CmdQuit }
func (Reload) Command() Command            { return CmdReload }
func (Password) Command() Command          { return CmdPassword }
func (CachedPassword) Command() Command    { return CmdCachedPassword }
func (Question) Command() Command          { return CmdQuestion }
func (ShowMessage) Command() Command       { return CmdShowMessage }
func (HideMessage) Command() Command       { return CmdHideMessage }
func (WatchKeystroke) Command() Command    { return CmdWatchKeystroke }
func (IgnoreKeystroke) Command() Command   { return CmdIgnoreKeystroke }
func (PauseProgress) Command() Command     { return CmdPauseProgress }
func (ResumeProgress) Command() Command    { return CmdResumeProgress }
func (ShowSplash) Command() Command        { return CmdShowSplash }
func (HideSplash) Command() Command        { return CmdHideSplash }
func (NewRoot) Command() Command           { return CmdNewRoot }
func (HasActiveVT) Command() Command       { return CmdHasActiveVT }
func (Error) Command() Command             { return CmdError }

func none() (string, bool) { return "", false }

func text(s string) (string, bool) { return s, s != "" }

func (Ping) argument() (string, bool)              { return none() }
func (r Update) argument() (string, bool)          { return text(r.Status) }
func (r ChangeMode) argument() (string, bool)      { return text(r.Mode) }
func (r SystemUpdate) argument() (string, bool)    { return strconv.Itoa(int(r.Progress)), true }
func (SystemInitialized) argument() (string, bool) { return none() }
func (Deactivate) argument() (string, bool)        { return none() }
func (Reactivate) argument() (string, bool)        { return none() }
func (Reload) argument() (string, bool)            { return none() }
func (r Password) argument() (string, bool)        { return text(r.Prompt) }
func (CachedPassword) argument() (string, bool)    { return none() }
func (r Question) argument() (string, bool)        { return text(r.Prompt) }
func (r ShowMessage) argument() (string, bool)     { return text(r.Text) }
func (r HideMessage) argument() (string, bool)     { return text(r.Text) }
func (r WatchKeystroke) argument() (string, bool)  { return text(r.Keys) }
func (r IgnoreKeystroke) argument() (string, bool) { return text(r.Keys) }
func (PauseProgress) argument() (string, bool)     { return none() }
func (ResumeProgress) argument() (string, bool)    { return none() }
func (ShowSplash) argument() (string, bool)        { return none() }
func (HideSplash) argument() (string, bool)        { return none() }
func (r NewRoot) argument() (string, bool)         { return text(r.Dir) }
func (HasActiveVT) argument() (string, bool)       { return none() }
func (Error) argument() (string, bool)             { return none() }

func (r Quit) argument() (string, bool) {
	if r.Retain {
		return "\x01", true
	}
	return "", true
}

// Percent is a progress in percent, 0 to 100.
type Percent uint8

// MaxPercent is the largest Percent, a finished update.
const MaxPercent Percent = 100

// ParsePercent reads a decimal percentage as plymouthd accepts it: digits
// only, 0 to 100.
func ParsePercent(s string) (Percent, error) {
	if s == "" || s[0] == '+' || s[0] == '-' {
		return 0, &strconv.NumError{Func: "ParsePercent", Num: s, Err: strconv.ErrSyntax}
	}
	v, err := strconv.ParseUint(s, 10, 8)
	if err != nil {
		return 0, err
	}
	if v > uint64(MaxPercent) {
		return 0, &strconv.NumError{Func: "ParsePercent", Num: s, Err: strconv.ErrRange}
	}
	return Percent(v), nil
}
