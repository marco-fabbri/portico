package settings

// NetworkingOperations are what the service account needs beside Project
// Manager for tenant ready networks (tenant networking design 4.5,
// write-up 34) and for the site's BGP peering (prefix-routing design 4.7,
// write-up 38): the tenant's subnet in the transit VPC, its sharing,
// floating IPs and the VPC's default route; the BGP gateways and session,
// their routes; the edge router's image, VM and category; the external
// subnet's gateway; the cluster the edge is placed on.
var NetworkingOperations = []string{
	"View_Subnet", "Create_Subnet", "Delete_Subnet", "Update_Subnet", "Share_Subnet", "Unshare_Subnet", "View_VPC",
	"View_Floating_Ip", "Create_Floating_Ip", "Update_Floating_Ip", "Delete_Floating_Ip",
	"View_VPC_Route_Table", "View_Route_Table_Route", "Create_Route_Table_Route", "Delete_Route_Table_Route",
	"View_Network_Gateway", "Create_Network_Gateway", "Update_Network_Gateway", "Delete_Network_Gateway",
	"View_Bgp_Session", "Create_Bgp_Session", "Update_Bgp_Session", "Delete_Bgp_Session", "View_Bgp_Route",
	"View_Image", "Create_Image", "Delete_Image",
	"View_Virtual_Machine", "Create_Virtual_Machine", "Update_Virtual_Machine", "Delete_Virtual_Machine",
	"Allow_Virtual_Machine_Power_On", "Allow_Virtual_Machine_Power_Off", "Update_Virtual_Machine_Categories",
	"View_Category", "Create_Category",
	"View_Cluster",
}
