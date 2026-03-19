package main

import (
	"flag"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

const (
	ifDescrOID       = "1.3.6.1.2.1.2.2.1.2"
	ifOperStatusOID  = "1.3.6.1.2.1.2.2.1.8"
	ifInOctetsOID    = "1.3.6.1.2.1.2.2.1.10"
	ifOutOctetsOID   = "1.3.6.1.2.1.2.2.1.16"
	ifHCInOctetsOID  = "1.3.6.1.2.1.31.1.1.1.6"
	ifHCOutOctetsOID = "1.3.6.1.2.1.31.1.1.1.10"
)

type Interface struct {
	Index  int
	Name   string
	Status int
	In     uint64
	Out    uint64
}

const keepCyclesAfterTraffic = 8

func main() {
	host := flag.String("host", "127.0.0.1", "SNMP host")
	community := flag.String("community", "public", "SNMP community")
	interval := flag.Int("interval", 1, "Polling interval in seconds")
	flag.Parse()

	if *interval <= 0 {
		fmt.Println("interval must be > 0")
		return
	}

	snmp := &gosnmp.GoSNMP{
		Target:    *host,
		Port:      161,
		Community: *community,
		Version:   gosnmp.Version2c,
		Timeout:   2 * time.Second,
		Retries:   1,
	}

	if err := snmp.Connect(); err != nil {
		fmt.Println("SNMP connection failed. Make sure snmpd is running.")
		return
	}
	defer snmp.Conn.Close()

	prev := map[int]Interface{}
	keepCycles := map[int]int{}
	lastInSpeed := map[int]uint64{}
	lastOutSpeed := map[int]uint64{}
	for {
		current, err := fetchInterfaces(snmp)
		if err != nil {
			fmt.Printf("SNMP error: %v\n", err)
			time.Sleep(time.Duration(*interval) * time.Second)
			continue
		}

		printInterfaces(current, prev, keepCycles, lastInSpeed, lastOutSpeed, *interval)
		prev = current
		time.Sleep(time.Duration(*interval) * time.Second)
	}
}

func fetchInterfaces(snmp *gosnmp.GoSNMP) (map[int]Interface, error) {
	interfaces := make(map[int]Interface)

	if err := walkAndSet(snmp, ifDescrOID, interfaces, func(iface *Interface, value interface{}) {
		iface.Name = toString(value)
	}); err != nil {
		return nil, err
	}

	if err := walkAndSet(snmp, ifOperStatusOID, interfaces, func(iface *Interface, value interface{}) {
		iface.Status = int(toUint64(value))
	}); err != nil {
		return nil, err
	}

	if err := walkAndSet(snmp, ifInOctetsOID, interfaces, func(iface *Interface, value interface{}) {
		iface.In = toUint64(value)
	}); err != nil {
		return nil, err
	}

	if err := walkAndSet(snmp, ifOutOctetsOID, interfaces, func(iface *Interface, value interface{}) {
		iface.Out = toUint64(value)
	}); err != nil {
		return nil, err
	}

	_ = walkAndSet(snmp, ifHCInOctetsOID, interfaces, func(iface *Interface, value interface{}) {
		iface.In = toUint64(value)
	})
	_ = walkAndSet(snmp, ifHCOutOctetsOID, interfaces, func(iface *Interface, value interface{}) {
		iface.Out = toUint64(value)
	})

	return interfaces, nil
}

func walkAndSet(snmp *gosnmp.GoSNMP, oid string, interfaces map[int]Interface, set func(*Interface, interface{})) error {
	pdus, err := snmp.WalkAll(oid)
	if err != nil {
		return err
	}

	for _, pdu := range pdus {
		idx, err := oidIndex(pdu.Name)
		if err != nil {
			continue
		}

		iface := interfaces[idx]
		iface.Index = idx
		set(&iface, pdu.Value)
		interfaces[idx] = iface
	}

	return nil
}

type ViewRow struct {
	Name   string
	Status string
	In     uint64
	Out    uint64
}

func printInterfaces(
	current map[int]Interface,
	prev map[int]Interface,
	keepCycles map[int]int,
	lastInSpeed map[int]uint64,
	lastOutSpeed map[int]uint64,
	interval int,
) {
	fmt.Println(strings.Repeat("-", 64))
	fmt.Println(time.Now().Format("2006-01-02 15:04:05"))

	indices := make([]int, 0, len(current))
	for idx := range current {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	rows := map[string]ViewRow{}
	hasBaseline := len(prev) > 0

	printed := 0

	for _, idx := range indices {
		cur := current[idx]
		old, ok := prev[idx]
		if !ok {
			continue
		}

		inSpeed := uint64(0)
		outSpeed := uint64(0)
		if cur.In >= old.In {
			inSpeed = (cur.In - old.In) / uint64(interval)
		}
		if cur.Out >= old.Out {
			outSpeed = (cur.Out - old.Out) / uint64(interval)
		}

		if inSpeed > 0 || outSpeed > 0 {
			keepCycles[idx] = keepCyclesAfterTraffic
		} else if keepCycles[idx] > 0 {
			keepCycles[idx]--
		}

		if inSpeed > 0 {
			lastInSpeed[idx] = inSpeed
		} else if keepCycles[idx] > 0 {
			inSpeed = lastInSpeed[idx]
		}

		if outSpeed > 0 {
			lastOutSpeed[idx] = outSpeed
		} else if keepCycles[idx] > 0 {
			outSpeed = lastOutSpeed[idx]
		}

		status := "DOWN"
		if cur.Status == 1 {
			status = "UP"
		}

		if inSpeed == 0 && outSpeed == 0 && keepCycles[idx] == 0 {
			continue
		}

		name := cur.Name
		if name == "" {
			name = fmt.Sprintf("if%d", cur.Index)
		}
		name = shortName(name, 40)

		row := rows[name]
		if row.Name == "" {
			row = ViewRow{Name: name, Status: status}
		}
		if status == "UP" {
			row.Status = "UP"
		}
		row.In += inSpeed
		row.Out += outSpeed
		rows[name] = row
	}

	names := make([]string, 0, len(rows))
	for name := range rows {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		row := rows[name]
		fmt.Printf("%-40s | %-4s | IN: %-10s | OUT: %-10s\n",
			row.Name,
			row.Status,
			formatSpeed(row.In),
			formatSpeed(row.Out),
		)
		printed++
	}

	if printed == 0 {
		if !hasBaseline {
			fmt.Println("Collecting baseline...")
		} else {
			fmt.Println("No traffic right now (all interfaces are 0 B/s)")
		}
	}

	for idx := range keepCycles {
		if _, ok := current[idx]; !ok {
			delete(keepCycles, idx)
			delete(lastInSpeed, idx)
			delete(lastOutSpeed, idx)
		}
	}
}

func oidIndex(oid string) (int, error) {
	pos := strings.LastIndex(oid, ".")
	if pos == -1 || pos == len(oid)-1 {
		return 0, fmt.Errorf("bad oid: %s", oid)
	}
	return strconv.Atoi(oid[pos+1:])
}

func toString(v interface{}) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
}

func toUint64(v interface{}) uint64 {
	bi := gosnmp.ToBigInt(v)
	if bi.Sign() < 0 {
		return 0
	}
	return bi.Uint64()
}

func formatSpeed(bytesPerSec uint64) string {
	if bytesPerSec < 1024 {
		return fmt.Sprintf("%d B/s", bytesPerSec)
	}
	kb := float64(bytesPerSec) / 1024
	if kb < 1024 {
		return fmt.Sprintf("%.0f KB/s", kb)
	}
	mb := kb / 1024
	return fmt.Sprintf("%.2f MB/s", mb)
}

func shortName(name string, max int) string {
	if max <= 3 || len(name) <= max {
		return name
	}
	return name[:max-3] + "..."
}
