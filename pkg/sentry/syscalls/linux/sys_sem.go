// Copyright 2018 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package linux

import (
	"math"

	"gvisor.googlesource.com/gvisor/pkg/abi/linux"
	"gvisor.googlesource.com/gvisor/pkg/sentry/arch"
	"gvisor.googlesource.com/gvisor/pkg/sentry/fs"
	"gvisor.googlesource.com/gvisor/pkg/sentry/kernel"
	"gvisor.googlesource.com/gvisor/pkg/sentry/kernel/auth"
	"gvisor.googlesource.com/gvisor/pkg/syserror"
)

const opsMax = 500 // SEMOPM

// Semget handles: semget(key_t key, int nsems, int semflg)
func Semget(t *kernel.Task, args arch.SyscallArguments) (uintptr, *kernel.SyscallControl, error) {
	key := args[0].Int()
	nsems := args[1].Int()
	flag := args[2].Int()

	private := key == linux.IPC_PRIVATE
	create := flag&linux.IPC_CREAT == linux.IPC_CREAT
	exclusive := flag&linux.IPC_EXCL == linux.IPC_EXCL
	mode := linux.FileMode(flag & 0777)

	r := t.IPCNamespace().SemaphoreRegistry()
	set, err := r.FindOrCreate(t, key, nsems, mode, private, create, exclusive)
	if err != nil {
		return 0, nil, err
	}
	return uintptr(set.ID), nil, nil
}

// Semop handles: semop(int semid, struct sembuf *sops, size_t nsops)
func Semop(t *kernel.Task, args arch.SyscallArguments) (uintptr, *kernel.SyscallControl, error) {
	id := args[0].Int()
	sembufAddr := args[1].Pointer()
	nsops := args[2].SizeT()

	r := t.IPCNamespace().SemaphoreRegistry()
	set := r.FindByID(id)
	if set == nil {
		return 0, nil, syserror.EINVAL
	}
	if nsops <= 0 {
		return 0, nil, syserror.EINVAL
	}
	if nsops > opsMax {
		return 0, nil, syserror.E2BIG
	}

	ops := make([]linux.Sembuf, nsops)
	if _, err := t.CopyIn(sembufAddr, ops); err != nil {
		return 0, nil, err
	}

	creds := auth.CredentialsFromContext(t)
	for {
		ch, num, err := set.ExecuteOps(t, ops, creds)
		if ch == nil || err != nil {
			// We're done (either on success or a failure).
			return 0, nil, err
		}
		if err = t.Block(ch); err != nil {
			set.AbortWait(num, ch)
			return 0, nil, err
		}
	}
}

// Semctl handles: semctl(int semid, int semnum, int cmd, ...)
func Semctl(t *kernel.Task, args arch.SyscallArguments) (uintptr, *kernel.SyscallControl, error) {
	id := args[0].Int()
	num := args[1].Int()
	cmd := args[2].Int()

	switch cmd {
	case linux.SETVAL:
		val := args[3].Int()
		if val > math.MaxInt16 {
			return 0, nil, syserror.ERANGE
		}
		return 0, nil, setVal(t, id, num, int16(val))

	case linux.GETVAL:
		v, err := getVal(t, id, num)
		return uintptr(v), nil, err

	case linux.IPC_RMID:
		return 0, nil, remove(t, id)

	case linux.IPC_SET:
		arg := args[3].Pointer()
		s := linux.SemidDS{}
		if _, err := t.CopyIn(arg, &s); err != nil {
			return 0, nil, err
		}

		perms := fs.FilePermsFromMode(linux.FileMode(s.SemPerm.Mode & 0777))
		return 0, nil, ipcSet(t, id, auth.UID(s.SemPerm.UID), auth.GID(s.SemPerm.GID), perms)

	default:
		return 0, nil, syserror.EINVAL
	}
}

func remove(t *kernel.Task, id int32) error {
	r := t.IPCNamespace().SemaphoreRegistry()
	creds := auth.CredentialsFromContext(t)
	return r.RemoveID(id, creds)
}

func ipcSet(t *kernel.Task, id int32, uid auth.UID, gid auth.GID, perms fs.FilePermissions) error {
	r := t.IPCNamespace().SemaphoreRegistry()
	set := r.FindByID(id)
	if set == nil {
		return syserror.EINVAL
	}

	creds := auth.CredentialsFromContext(t)
	kuid := creds.UserNamespace.MapToKUID(uid)
	if !kuid.Ok() {
		return syserror.EINVAL
	}
	kgid := creds.UserNamespace.MapToKGID(gid)
	if !kgid.Ok() {
		return syserror.EINVAL
	}
	owner := fs.FileOwner{UID: kuid, GID: kgid}
	return set.Change(t, creds, owner, perms)
}

func setVal(t *kernel.Task, id int32, num int32, val int16) error {
	r := t.IPCNamespace().SemaphoreRegistry()
	set := r.FindByID(id)
	if set == nil {
		return syserror.EINVAL
	}
	creds := auth.CredentialsFromContext(t)
	return set.SetVal(t, num, val, creds)
}

func getVal(t *kernel.Task, id int32, num int32) (int16, error) {
	r := t.IPCNamespace().SemaphoreRegistry()
	set := r.FindByID(id)
	if set == nil {
		return 0, syserror.EINVAL
	}
	creds := auth.CredentialsFromContext(t)
	return set.GetVal(num, creds)
}
