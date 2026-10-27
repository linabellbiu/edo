package k3s

import "k8s.io/client-go/tools/clientcmd"

// 只改当前上下文引用的集群，不能重写同一文件中其他集群的地址。
func rewriteKubeconfigEndpoint(raw []byte, endpoint string) (string, error) {
	config, err := clientcmd.Load(raw)
	if err != nil || config.CurrentContext == "" {
		return "", ErrRemote
	}
	selected := config.Contexts[config.CurrentContext]
	if selected == nil || selected.Cluster == "" {
		return "", ErrRemote
	}
	cluster := config.Clusters[selected.Cluster]
	if cluster == nil {
		return "", ErrRemote
	}
	cluster.Server = endpoint
	encoded, err := clientcmd.Write(*config)
	if err != nil {
		return "", ErrRemote
	}
	return string(encoded), nil
}
