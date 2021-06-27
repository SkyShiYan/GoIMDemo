package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	rawHeaderLen = 16
	packetOffset = 0
	headerOffset = 4
	verOffset    = 6
	opOffset     = 8
	seqOffset    = 12
)

var seq int32 = 1

func main() {
	fmt.Println("Go Main Start")
	wg := sync.WaitGroup{}
	ctx, cancel := context.WithCancel(context.Background())

	sigs := make(chan os.Signal, 1)
	// signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	// signal.Notify(sigs)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGKILL, syscall.SIGTERM)

	// 启动监听
	go startSocket(ctx)

	wg.Add(1)
	go func() {
		sig := <-sigs
		fmt.Println("收到CTRL+C signal信号，开始通知服务关闭", sig)
		cancel()
		time.Sleep(1 * time.Second)
		wg.Done()
	}()

	wg.Wait()
	fmt.Println("关闭")
}

func startSocket(ctx context.Context) {
	conn, err := net.Dial("tcp", "192.168.1.235:3101")
	if err != nil {
		log.Panicf("connect error:%X", err)
		return
	}

	defer conn.Close()

	fmt.Println("connect success")

	aR := make(chan bool)

	// 消息接收监听
	go read(conn, aR)
	// 注册
	go auth(conn)

	select {
	case <-aR:
	case <-time.After(5 * time.Second):
		log.Panicln("auth fail.")
	}

	// 第二步发送PING
	go ping(ctx, conn)

	select {
	case <-ctx.Done():
		conn.Close()
	}
}

func auth(conn net.Conn) {
	var msg = "{\"mid\":111, \"room_id\":\"live://1000\", \"platform\":\"web\", \"accepts\":[1000,1001,1002]}"
	err := write(conn, msg, 7)
	if err != nil {
		log.Panicf("auth error:%X", err)
		return
	}
}

func ping(ctx context.Context, conn net.Conn) {
	var index int = 1
	for {
		if index > 10 {
			fmt.Println("PING发送失败，需要重新AUTH")
			return
		}

		err := write(conn, "", 2)
		if err != nil {
			fmt.Printf("发送PING失败: %s", err)
			fmt.Println()
			time.After(1 * time.Second)
			continue
		}

		fmt.Println("发送PING成功")

		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func write(conn net.Conn, msg string, msgType int32) error {
	// 组装发送的数据
	// 1.PageLength
	// 2.HeadLength
	// 3.ProtocalVersion
	// 4.Operation
	// 5.Sequence Id
	// 6.Body
	body := stringToByte(msg)
	pageL := Int32ToBytes(int32(rawHeaderLen + len(body)))
	head := Int16ToBytes(int16(rawHeaderLen))
	pv := Int16ToBytes(1)
	op := Int32ToBytes(msgType)
	seqB := Int32ToBytes(seq)

	request := make([]byte, rawHeaderLen+len(body))
	for i := 0; i < 4; i++ {
		request[i] = pageL[i]
	}
	for i := 0; i < 2; i++ {
		request[i+len(pageL)] = head[i]
	}
	for i := 0; i < 2; i++ {
		request[i+len(pageL)+len(head)] = pv[i]
	}
	for i := 0; i < 4; i++ {
		request[i+len(pageL)+len(head)+len(pv)] = op[i]
	}
	for i := 0; i < 4; i++ {
		request[i+len(pageL)+len(head)+len(pv)+len(op)] = seqB[i]
	}
	for i := 0; i < len(body); i++ {
		request[i+len(pageL)+len(head)+len(pv)+len(op)+len(seqB)] = body[i]
	}

	_, err := conn.Write(request)
	// encodedStr := hex.EncodeToString(request)
	// fmt.Printf("nnn %s", encodedStr)
	// fmt.Println()
	fmt.Printf("write data %s", hex.EncodeToString(request))
	fmt.Println()

	seq++

	return err
}

func readAuth(conn net.Conn, ch chan byte) {
	readBytes := make([]byte, 8)
	for {
		conn.SetReadDeadline(time.Now().Add(100 * time.Microsecond))
		if _, err := conn.Read(readBytes); err != nil {
			fmt.Printf("Read error: %s", err)
			fmt.Println("")
			close(ch)
			return
		}

		fmt.Printf("Read reponse hex: %s", hex.EncodeToString(readBytes))
		fmt.Println("")

		ch <- readBytes[0]
	}
}

func read(conn net.Conn, ch chan bool) {
	readL := 16
	headA := make([]byte, readL)
	var bodyA []byte

	IsHead := true

	for {
		readBytes := make([]byte, readL)

		if _, err := conn.Read(readBytes); err != nil {
			if strings.Contains(err.Error(), "timeout") {
				fmt.Println("Read Time out.")
				// conn.SetReadDeadline(time.Date(0, 0, 0, 0, 0, 0, 0, time.Now().Location()))
				continue
			} else {
				fmt.Printf("Read error: %s", err)
				fmt.Println("")
				return
			}
		}

		fmt.Printf("Read reponse hex: %s", hex.EncodeToString(readBytes))
		fmt.Println("")

		if IsHead {
			// 说明我当前读取的数据是数据头，固定长度16个字节
			// TODO:根据我当前的数据头中的长度反推我的body长度，然后去设置我下一次读取的长度，设置超时
			if len(readBytes) != 16 {
				continue
			}

			headA = readBytes
			var packetLen = BytesToInt32(headA[0:4])
			var headerLen = BytesToInt16(headA[4:6])
			var ver = BytesToInt16(headA[6:8])
			var op = BytesToInt32(headA[8:12])
			var seq = BytesToInt32(headA[12:16])

			fmt.Printf("head信息 %s %s %d", hex.EncodeToString(headA[0:]), hex.EncodeToString(headA[8:12]), op)
			fmt.Println("")

			switch op {
			case 8:
				ch <- true
				IsHead = true
				readL = 16
			case 3:
				fmt.Println("读取PING返回")
				IsHead = true
				readL = 16
				break
			case 9:
				fmt.Println("batch message")
				readL = packetLen - headerLen
				IsHead = false
				break
			default:
				fmt.Printf("收到，开始获取新的message %d", packetLen-headerLen)
				fmt.Println()
				IsHead = false
				readL = packetLen - headerLen
				break
			}
			fmt.Printf("packetLen: %d headerLen: %d ver: %d op: %d seq: %d", packetLen, headerLen, ver, op, seq)
			fmt.Println()
		} else {
			bodyA = readBytes
			fmt.Println("message:" + byteToString(bodyA))
			IsHead = true
			readL = 16
		}
	}
}

// string转为[]byte
func stringToByte(msg string) []byte {
	return []byte(msg)
}

// byte转为string
func byteToString(msg []byte) string {
	return string(msg[:])
}

// 整形转换成字节
func Int32ToBytes(n int32) []byte {
	bytesBuffer := bytes.NewBuffer([]byte{})
	binary.Write(bytesBuffer, binary.BigEndian, n)
	return bytesBuffer.Bytes()
}
func Int16ToBytes(n int16) []byte {
	bytesBuffer := bytes.NewBuffer([]byte{})
	binary.Write(bytesBuffer, binary.BigEndian, n)
	return bytesBuffer.Bytes()
}

// 字节转换成整形
func BytesToInt32(b []byte) int {
	bytesBuffer := bytes.NewBuffer(b)
	var x int32
	binary.Read(bytesBuffer, binary.BigEndian, &x)
	return int(x)
}

// 字节转换成整形
func BytesToInt16(b []byte) int {
	bytesBuffer := bytes.NewBuffer(b)
	var x int16
	binary.Read(bytesBuffer, binary.BigEndian, &x)
	return int(x)
}
