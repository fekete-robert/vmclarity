import React from 'react';
import { useLocation } from 'react-router-dom';
import DetailsPageWrapper from 'components/DetailsPageWrapper';
import TabbedPage from 'components/TabbedPage';
import { APIS } from 'utils/systemConsts';
import { formatDate } from 'utils/utils';
// import ScanActionsDisplay from '../ScanActionsDisplay';
import { ScanDetails as ScanDetailsTab, Findings } from 'layout/detail-displays';

export const SCAN_DETAILS_PATHS = {
    SCAN_DETALS: "",
    FINDINGS: "findings"
}

const DetailsContent = ({data}) => {
    const {pathname} = useLocation();
    
    const {id} = data;
    
    return (
        <TabbedPage
            basePath={`${pathname.substring(0, pathname.indexOf(id))}${id}`}
            items={[
                {
                    id: "general",
                    title: "Scan details",
                    isIndex: true,
                    component: () => <ScanDetailsTab scanData={data} withAssetScansLink />
                },
                {
                    id: "findings",
                    title: "Findings",
                    path: SCAN_DETAILS_PATHS.FINDINGS,
                    component: () => <Findings findingsSummary={data?.summary} />
                }
            ]}
            // headerCustomDisplay={() => (
            //     <ScanActionsDisplay data={data} />
            // )}
            withInnerPadding={false}
        />
    )
}

const ScanDetails = () => (
    <DetailsPageWrapper
        className="scan-details-page-wrapper"
        backTitle="Scans"
        getUrl={({id}) => `${APIS.SCANS}/${id}?$expand=scanConfig`}
        getTitleData={({scanConfigSnapshot, startTime}) => ({title: scanConfigSnapshot?.name, subTitle: formatDate(startTime)})}
        detailsContent={props => <DetailsContent {...props} />}
    />
)

export default ScanDetails;