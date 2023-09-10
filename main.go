package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/go-ping/ping"
)

var (
	Concurrency = flag.Int("conc", 10, "How many goroutines are executed")
	PingTimeout = flag.Int("time", 2, "ICMP responce timeout in seconds")
	OutFilename = flag.String("outFile", "netmap.puml", "a PlantUML file with list of hosts")
	Subnet      = flag.String("range", "192.168.0.0/24", "the range of scanned hosts in form of '192.168.0.0/24'")
)

func main() {

	flag.Parse()

	ipNet, err := parseSubnet(*Subnet)
	if err != nil {
		fmt.Println("Ошибка разбора подсети:", err)
		return
	}

	activeHosts := scanSubnet(ipNet, *Concurrency)

	fmt.Println("Список активных хостов:")
	hosts := sortActiveHosts(activeHosts)
	for _, host := range hosts {
		name := activeHosts[host]
		fmt.Printf("%s[address = \"%s\"];\n", name, host)
	}

	// Вызываем функцию для записи в файл
	err = writeToFile(*OutFilename, activeHosts)
	if err != nil {
		fmt.Println("Ошибка записи в файл:", err)
	}
}

func parseSubnet(subnet string) (*net.IPNet, error) {
	_, ipNet, err := net.ParseCIDR(subnet)
	return ipNet, err
}

func scanSubnet(ipNet *net.IPNet, concurrency int) map[string]string {
	activeHosts := &sync.Map{}
	hostNames := make(map[string]string)
	var wg sync.WaitGroup
	addresses := generateIPs(ipNet)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			scanSubnetWorker(activeHosts, addresses, id, hostNames)
		}(i)
	}

	wg.Wait()

	// Создайте карту для хранения имен хостов
	hostNameMap := make(map[string]string)
	activeHosts.Range(func(key, value interface{}) bool {
		host := key.(string)
		if name, ok := hostNames[host]; ok {
			hostNameMap[host] = name
		}
		return true
	})

	return hostNameMap
}

func getHostName(host string) (string, error) {
	snmpName, err := querySNMPName(host)
	if err == nil && snmpName != "" {
		return snmpName, nil
	}

	netbiosName, err := queryNetBIOSName(host)
	if err == nil && netbiosName != "" {
		return netbiosName, nil
	}

	return host, nil
}
func generateIPs(ipNet *net.IPNet) <-chan string {
	addresses := make(chan string)

	go func() {
		defer close(addresses)

		for ip := ipNet.IP.Mask(ipNet.Mask); ipNet.Contains(ip); incrementIP(ip) {
			// Пропускаем адреса .0 и .255
			if ip[3] == 0 || ip[3] == 255 {
				continue
			}
			addresses <- ip.String()
		}
	}()

	return addresses
}

func scanSubnetWorker(activeHosts *sync.Map, addresses <-chan string, id int, hostNames map[string]string) {
	for host := range addresses {
		if isHostActive(host) {
			activeHosts.Store(host, true)
			name, err := getHostName(host)
			if err == nil {
				hostNames[host] = name
			}
		}
		fmt.Printf("Горутина %d сканирует: %s\n", id, host)
	}
}

func isHostActive(host string) bool {
	// Проверяем активность хоста с помощью ICMP
	pinger, err := ping.NewPinger(host)
	if err != nil {
		fmt.Println("Ошибка создания Pinger:", err)
		return false
	}
	pinger.SetPrivileged(true)
	pinger.Count = 1
	pinger.Timeout = timeoutDuration(*PingTimeout)

	err = pinger.Run()
	if err != nil {
		fmt.Println("Ошибка выполнения ICMP-запроса:", err)
		return false
	}

	active := pinger.Statistics().PacketsRecv > 0

	// Опрашиваем имя хоста через SNMP
	if active {
		fmt.Printf("Опрашиваем хост %s\n", host)
		snmpName, err := querySNMPName(host)
		if err == nil {
			fmt.Printf("Имя хоста %s: %s\n", host, snmpName)
		} else {
			fmt.Println(err)
		}
		netbiosName, err := queryNetBIOSName(host)
		if err == nil {
			fmt.Printf("Имя хоста %s: %s\n", host, netbiosName)
		} else {
			fmt.Println(err)
		}
	}

	return active
}

func queryNetBIOSName(host string) (string, error) {
	cmd := exec.Command("nbtscan", host)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, host) {
			parts := strings.Fields(line)
			if len(parts) > 1 {
				return parts[1], nil
			}
		}
	}

	return "", fmt.Errorf("не удалось получить имя хоста через nbtscan")
}

func querySNMPName(host string) (string, error) {
	params := &gosnmp.GoSNMP{
		Target:    host,
		Port:      161,
		Community: "public", // Замените на ваше сообщество SNMP
		Version:   gosnmp.Version2c,
	}

	err := params.Connect()
	if err != nil {
		return "", err
	}
	defer params.Conn.Close()

	oid := ".1.3.6.1.2.1.1.5.0" // OID для имени хоста

	response, err := params.Get([]string{oid})
	if err != nil {
		return "", err
	}

	if len(response.Variables) > 0 {
		return response.Variables[0].Value.(string), nil
	}

	return "", fmt.Errorf("не удалось получить имя хоста")
}

func timeoutDuration(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}

func incrementIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

func printSortedActiveHosts(activeHosts map[string]string) {
	// Создаем слайс для сортировки
	var hosts []string

	for host := range activeHosts {
		hosts = append(hosts, host)
	}

	// Определяем функцию сравнения для сортировки
	sort.Slice(hosts, func(i, j int) bool {
		ip1 := net.ParseIP(hosts[i]).To4()
		ip2 := net.ParseIP(hosts[j]).To4()
		return compareIPs(ip1, ip2) < 0
	})

	// Выводим отсортированный список
	for _, host := range hosts {
		fmt.Printf("%s[address = \"%s\"];\n", activeHosts[host], host)
	}
}

// Функция для сортировки активных хостов по IP-адресам
func sortActiveHosts(activeHosts map[string]string) []string {
	// Создаем слайс для сортировки
	var hosts []string

	for host := range activeHosts {
		hosts = append(hosts, host)
	}

	// Определяем функцию сравнения для сортировки
	sort.Slice(hosts, func(i, j int) bool {
		ip1 := net.ParseIP(hosts[i]).To4()
		ip2 := net.ParseIP(hosts[j]).To4()
		return compareIPs(ip1, ip2) < 0
	})

	return hosts
}

// Функция для сравнения двух IP-адресов
func compareIPs(ip1, ip2 net.IP) int {
	// Преобразовываем IP-адреса в числовой формат
	ip1Parts := strings.Split(ip1.String(), ".")
	ip2Parts := strings.Split(ip2.String(), ".")

	for i := 0; i < 4; i++ {
		ip1Value, _ := strconv.Atoi(ip1Parts[i])
		ip2Value, _ := strconv.Atoi(ip2Parts[i])

		if ip1Value < ip2Value {
			return -1
		} else if ip1Value > ip2Value {
			return 1
		}
	}

	return 0
}

// Функция для записи активных хостов в файл
func writeToFile(fileName string, activeHosts map[string]string) error {
	file, err := os.Create(fileName)
	if err != nil {
		return err
	}
	defer file.Close()

	// Получаем отсортированный список хостов
	hosts := sortActiveHosts(activeHosts)

	for _, host := range hosts {
		name := activeHosts[host]
		line := fmt.Sprintf("%s[address = \"%s\"];\n", name, host)
		_, err := file.WriteString(line)
		if err != nil {
			return err
		}
	}

	return nil
}
