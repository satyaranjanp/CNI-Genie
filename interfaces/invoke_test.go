package interfaces

import (
	"github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/types"
)

type FakeInvoke struct {
	Path   *CniPath
	Result *types.Result
	Error  error
}

func (i *FakeInvoke) InvokeExecAdd(config, rtConf *libcni.RuntimeConf) (*types.Result, error) {
	return i.Result, i.Error
}

func (i *FakeInvoke) InvokeExecDel(config, rtConf *libcni.RuntimeConf) error {
	return i.Error
}
