package settings

// NetworkingOperations are what the service account needs beside Project
// Manager for tenant ready networks (tenant networking design 4.5,
// write-up 34): the tenant's subnet in the transit VPC, its sharing,
// floating IPs and the VPC's default route.
var NetworkingOperations = []string{
	"View_Subnet", "Create_Subnet", "Delete_Subnet", "Share_Subnet", "Unshare_Subnet", "View_VPC",
	"View_Floating_Ip", "Create_Floating_Ip", "Update_Floating_Ip", "Delete_Floating_Ip",
	"View_VPC_Route_Table", "View_Route_Table_Route", "Create_Route_Table_Route", "Delete_Route_Table_Route",
}
