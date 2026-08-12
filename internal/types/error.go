package types

import "errors"

// 自定义错误类型

var (
	// Container 已经停止
	ErrContainerAlreadyStopped = errors.New("container already stopped")
)
