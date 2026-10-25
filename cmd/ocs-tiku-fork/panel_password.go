package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"github.com/onprs/OCS-Tiku/internal/platform/panel"
)

// panelPassword 从标准输入或私密文件读取密码，参数和输出均不包含明文密码。
func panelPassword(args []string) error {
	flags := flag.NewFlagSet("panel-password", flag.ContinueOnError)
	output := flags.String("out", "", "新密码哈希文件路径")
	passwordFile := flags.String("password-file", "", "权限为0600的密码输入文件；省略时从标准输入读取")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *output == "" || flags.NArg() != 0 {
		return errors.New("用法：ocs-tiku-fork panel-password -out 路径 [-password-file 私密文件]")
	}
	var password string
	if *passwordFile != "" {
		value, err := config.ReadSecretFile(*passwordFile)
		if err != nil {
			return errors.New("密码输入文件不可用")
		}
		password = value
	} else {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 1025))
		if err != nil {
			return errors.New("无法读取维护密码")
		}
		password = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	}
	hash, err := panel.HashPassword(password)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("无法创建密码哈希文件；请使用尚不存在的文件路径")
	}
	if _, err := io.WriteString(file, hash+"\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(*output)
		return errors.New("无法保存密码哈希文件")
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(*output)
		return errors.New("无法保存密码哈希文件")
	}
	fmt.Println("维护密码哈希已保存")
	return nil
}
