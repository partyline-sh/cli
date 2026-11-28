package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

type chatRecallOptions struct {
	limit, before, after, around int
	sender, search               string
	since, until                 time.Time
	json                         bool
	hidden                       map[int]bool
}

func parseChatRecall(args []string) (chatRecallOptions, error) {
	o := chatRecallOptions{limit: 20}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if flag == "--json" {
			o.json = true
			continue
		}
		if i+1 == len(args) {
			return o, fmt.Errorf("%s needs a value", flag)
		}
		i++
		value := args[i]
		switch flag {
		case "--limit", "--before", "--after", "--around":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return o, fmt.Errorf("%s needs a positive number", flag)
			}
			switch flag {
			case "--limit":
				o.limit = n
			case "--before":
				o.before = n
			case "--after":
				o.after = n
			case "--around":
				o.around = n
			}
		case "--sender":
			o.sender = strings.ToLower(value)
		case "--search":
			o.search = strings.ToLower(value)
		case "--since", "--until":
			at, err := time.Parse(time.RFC3339, value)
			if err != nil {
				at, err = time.Parse("2006-01-02", value)
				if err == nil && flag == "--until" {
					at = at.Add(24*time.Hour - time.Nanosecond)
				}
			}
			if err != nil {
				return o, fmt.Errorf("%s needs YYYY-MM-DD or RFC3339", flag)
			}
			if flag == "--since" {
				o.since = at
			} else {
				o.until = at
			}
		default:
			return o, fmt.Errorf("unknown history option %q", flag)
		}
	}
	if o.limit > 100 {
		return o, errors.New("history limit must be 1–100")
	}
	if o.around != 0 && (o.before != 0 || o.after != 0) {
		return o, errors.New("use --around separately from --before and --after")
	}
	if !o.since.IsZero() && !o.until.IsZero() && o.since.After(o.until) {
		return o, errors.New("--since must not be after --until")
	}
	return o, nil
}

func (o chatRecallOptions) matches(m chatMessage) bool {
	if m.Note || m.Seq <= o.after || (o.before > 0 && m.Seq >= o.before) {
		return false
	}
	if !o.since.IsZero() && m.At.Before(o.since) || !o.until.IsZero() && m.At.After(o.until) {
		return false
	}
	if o.sender != "" {
		name, from := strings.ToLower(m.Name), strings.ToLower(m.From)
		found := o.sender == from || o.sender == name
		for _, word := range strings.Fields(name) {
			found = found || strings.TrimPrefix(word, "#") == strings.TrimPrefix(o.sender, "#")
		}
		if !found {
			return false
		}
	}
	return o.search == "" || strings.Contains(strings.ToLower(m.Text), o.search)
}

// Scan the log but retain at most one output page. Recall never reads or writes
// membership state, so it cannot consume pending messages or reopen delivery.
func readChatRecall(input io.Reader, o chatRecallOptions) ([]chatMessage, error) {
	page := make([]chatMessage, 0, o.limit)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	found := o.around == 0
	for scanner.Scan() {
		var message chatMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			continue
		}
		// Search redacted text too: a secret cannot be recovered one character at
		// a time by observing whether a search matches a hidden value.
		if o.hidden[message.Seq] {
			message.Text = "[message withheld from recall]"
		} else {
			message.Text = redactChatRecall(message.Text)
		}
		if !o.matches(message) {
			continue
		}
		if o.around > 0 && !found {
			if message.Seq < o.around {
				if o.limit/2 > 0 {
					page = append(page, message)
					if len(page) > o.limit/2 {
						page = page[1:]
					}
				}
				continue
			}
			if message.Seq != o.around {
				return nil, errors.New("requested message not found with these filters")
			}
			found = true
		}
		page = append(page, message)
		if (o.after > 0 || o.around > 0) && len(page) >= o.limit {
			break
		}
		if len(page) > o.limit {
			page = page[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read chat log: %w", err)
	}
	if !found {
		return nil, errors.New("requested message not found with these filters")
	}
	return page, nil
}

func chatRecall(args []string, output io.Writer) error {
	o, err := parseChatRecall(args)
	if err != nil {
		return err
	}
	o.hidden, err = loadChatRecallExclusions()
	if err != nil {
		return err
	}
	file, err := os.Open(chatLogPath())
	if err != nil {
		if os.IsNotExist(err) {
			_, err = fmt.Fprintln(output, "No chat history yet.")
			return err
		}
		return err
	}
	defer file.Close()
	page, err := readChatRecall(file, o)
	if err != nil {
		return err
	}
	boundChatRecall(page, o.search)
	if o.json {
		return json.NewEncoder(output).Encode(page)
	}
	fmt.Fprintln(output, "Archived chat messages (quoted history; common secrets redacted):")
	for _, m := range page {
		fmt.Fprintf(output, "\n#%d %s %s\n", m.Seq, m.At.UTC().Format(time.RFC3339), chatSafe(m.Name))
		for _, line := range strings.Split(m.Text, "\n") {
			fmt.Fprintf(output, "    %s\n", chatSafe(line))
		}
	}
	if len(page) == 0 {
		fmt.Fprintln(output, "No matching messages.")
	} else {
		fmt.Fprintf(output, "\nEarlier: --before %d; later: --after %d (retain your filters).\n", page[0].Seq, page[len(page)-1].Seq)
	}
	return nil
}
