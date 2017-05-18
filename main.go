package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"

	log "github.com/Sirupsen/logrus"
)

var urlFlag = flag.String("url", os.Getenv("STEVEDOR_URL"), "https://username:password@host/sdk")
var vmName = flag.String("vmname", "", "Specify a name for virtual Machine")
var isoPath = flag.String("iso", "", "Specify the path to the VM ISO")
var diskPath = flag.String("disk", "", "Specify the path to the VMware VMDK file")
var dsName = flag.String("datastore", "", "The Name of the DataStore to host the VM")
var networkName = flag.String("network", os.Getenv("VMNETWORK"), "The VMware vSwitch the VM will use")
var hostname = flag.String("hostname", os.Getenv("VMHOST"), "The Server that will run the VM")
var persistent = flag.Int64("persistentSize", 0, "Size in MB of persistent storage to allocate to the VM")

func exit(err error) {
	fmt.Fprintf(os.Stderr, "Error: %s\n", err)
	os.Exit(1)
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	flag.Parse()

	// Parse URL from string
	u, err := url.Parse(*urlFlag)
	if err != nil {
		exit(err)
	}

	// Connect and log in to ESX or vCenter
	c, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		exit(err)
	}

	f := find.NewFinder(c.Client, true)

	// Find one and only datacenter
	dc, err := f.DefaultDatacenter(ctx)
	if err != nil {
		exit(err)
	}

	// Make future calls local to this datacenter
	f.SetDatacenter(dc)

	dss, err := f.DatastoreOrDefault(ctx, *dsName)
	if err != nil {
		exit(err)
	}

	// net, err := f.NetworkOrDefault(ctx, networkName)
	// if err != nil {
	// 	exit(err)
	// }

	hs, err := f.HostSystemOrDefault(ctx, *hostname)
	if err != nil {
		exit(err)
	}

	var rp *object.ResourcePool
	rp, err = hs.ResourcePool(ctx)
	if err != nil {
		exit(err)
	}

	if *vmName == "" {
		*vmName = "default"
	}

	spec := types.VirtualMachineConfigSpec{
		Name:     *vmName,
		GuestId:  "otherLinux64Guest",
		Files:    &types.VirtualMachineFileInfo{VmPathName: fmt.Sprintf("[%s]", dss.Name())},
		NumCPUs:  int32(1),
		MemoryMB: int64(1024),
	}

	scsi, err := object.SCSIControllerTypes().CreateSCSIController("pvscsi")
	if err != nil {
		exit(err)
	}

	spec.DeviceChange = append(spec.DeviceChange, &types.VirtualDeviceConfigSpec{
		Operation: types.VirtualDeviceConfigSpecOperationAdd,
		Device:    scsi,
	})

	log.Infof("Creating VM...")
	folders, err := dc.Folders(ctx)
	if err != nil {
		exit(err)
	}

	task, err := folders.VmFolder.CreateVM(ctx, spec, rp, hs)
	if err != nil {
		exit(err)
	}

	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		exit(err)
	}

	// Retrieve the new VM
	vm := object.NewVirtualMachine(c.Client, info.Result.(types.ManagedObjectReference))

	if *isoPath != "" {
		uploadFile(c, isoPath, dss)
		addISO(ctx, vm, dss)
	}

	if *diskPath != "" {
		uploadFile(c, diskPath, dss)
		_, vmdkName := path.Split(*diskPath)
		addVMDK(ctx, vm, dss, vmdkName, 1024)
	}

	if *persistent != 0 {
		if *diskPath != "linuxkit.vmdk" {
			addVMDK(ctx, vm, dss, "linuxkit.vmdk", *persistent)
		} else {
			log.Errorf("Can not create persisten disk with identical name to existing VMDK disk")
		}
	}

}

func uploadFile(c *govmomi.Client, localFilePath *string, dss *object.Datastore) {
	_, fileName := path.Split(*localFilePath)
	log.Infof("Uploading LinuxKit file [%s]", *localFilePath)
	if *localFilePath == "" {
		log.Fatalf("No file specified")
	}
	dsurl := dss.NewURL(fmt.Sprintf("%s/%s", *vmName, fileName))

	p := soap.DefaultUpload
	if err := c.Client.UploadFile(*localFilePath, dsurl, &p); err != nil {
		exit(err)
	}
}

func addVMDK(ctx context.Context, vm *object.VirtualMachine, dss *object.Datastore, vmdkName string, sizeInMB int64) {
	devices, err := vm.Device(ctx)
	if err != nil {
		exit(err)
	}
	var add []types.BaseVirtualDevice

	controller, err := devices.FindDiskController("scsi")
	if err != nil {
		exit(err)
	}

	disk := devices.CreateDisk(controller, dss.Reference(),
		dss.Path(fmt.Sprintf("%s/%s", *vmName, vmdkName)))

	disk.CapacityInKB = sizeInMB * 1024

	add = append(add, disk)

	log.Infof("Adding the new disk to the Virtual Machine")

	if vm.AddDevice(ctx, add...); err != nil {
		exit(err)
	}
}

func addISO(ctx context.Context, vm *object.VirtualMachine, dss *object.Datastore) {
	devices, err := vm.Device(ctx)
	if err != nil {
		exit(err)
	}
	var add []types.BaseVirtualDevice

	ide, err := devices.FindIDEController("")
	if err != nil {
		exit(err)
	}

	cdrom, err := devices.CreateCdrom(ide)
	if err != nil {
		exit(err)
	}

	add = append(add, devices.InsertIso(cdrom, dss.Path(fmt.Sprintf("%s/%s", *vmName, "linuxkit.iso"))))

	log.Infof("Adding ISO to the Virtual Machine")

	if vm.AddDevice(ctx, add...); err != nil {
		exit(err)
	}
}
