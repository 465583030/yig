BASEDIR=$(dirname $(pwd))
sudo docker run --rm -ti  \
	                  \
			 -v ${BASEDIR}/integrate/cephconf:/etc/ceph/ \
			 -v ${BASEDIR}/integrate/yigconf:/etc/yig/ \
			 -v ${BASEDIR}:/work  \
			 -v ${BASEDIR}:/var/log/yig \
                         --net=integrate_vpcbr \
			 thesues/docker-ceph-devel /work/build/bin/delete
