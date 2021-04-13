Name: ovs-exporter
Version: 1.0.0
Release: 1
Summary: Standalone OVS Exporter

License: ASL 2.0
Source0: ovs-exporter.tar.gz

BuildArch: x86_64

%global debug_package %{nil}

%description
%{summary}

%prep
%setup -q -n ovs-exporter

%install
mkdir -p %{buildroot}/
install -p -m 0755 -D bin/ovn-kube-util %{buildroot}/%{_bindir}/ovn-kube-util
install -p -m 0644 -D bin/git_info %{buildroot}/etc/ovn-kube-util/git_info
install -p -m 0644 -D config/ovs-exporter.service %{buildroot}/etc/systemd/system/ovs-exporter.service
sed -i 's|<EXPORTER_PATH>|%{_bindir}|' %{buildroot}/etc/systemd/system/ovs-exporter.service
sed -i 's/<LISTENING_IP>/0.0.0.0/' %{buildroot}/etc/systemd/system/ovs-exporter.service

%files
%{_bindir}/ovn-kube-util
/etc/ovn-kube-util/git_info
/etc/systemd/system/ovs-exporter.service

%post
systemctl daemon-reload
systemctl enable ovs-exporter
systemctl start ovs-exporter

%preun
systemctl stop ovs-exporter
systemctl disable ovs-exporter

%postun
systemctl daemon-reload
