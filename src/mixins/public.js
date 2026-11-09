import axios from "axios";
import { baseURL } from "../auth-client";

axios.defaults.baseURL = baseURL;

export default {
    data() {
        return {
            publicGroupList: [],
        };
    },
    computed: {
        publicMonitorList() {
            let result = {};

            for (let group of this.publicGroupList) {
                for (let monitor of group.monitorList) {
                    result[monitor.id] = monitor;
                }
            }
            return result;
        },

        publicLastHeartbeatList() {
            let result = {};

            for (let monitorID in this.publicMonitorList) {
                if (this.lastHeartbeatList[monitorID]) {
                    result[monitorID] = this.lastHeartbeatList[monitorID];
                }
            }

            return result;
        },

        baseURL() {
            if (this.$root.info.primaryBaseURL) {
                return this.$root.info.primaryBaseURL;
            }

            return location.protocol + "//" + location.host;
        },
    },
};
