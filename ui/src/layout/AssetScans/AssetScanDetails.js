import React from 'react';
import { useLocation } from 'react-router-dom';
import DetailsPageWrapper from 'components/DetailsPageWrapper';
import TabbedPage from 'components/TabbedPage';
import { APIS } from 'utils/systemConsts';
import { formatDate } from 'utils/utils';
import { AssetDetails as AssetDetailsTab, Findings, ScanDetails as ScanDetailsTab } from 'layout/detail-displays';

const ASSET_SCAN_DETAILS_PATHS = {
    ASSET_DETAILS: "",
    SCAN_DETAILS: "scan",
    FINDINGHS: "findings"
}

const DetailsContent = ({data}) => {
    const {pathname} = useLocation();
    
    const {id, scan, target} = data;
    
    return (
        <TabbedPage
            basePath={`${pathname.substring(0, pathname.indexOf(id))}${id}`}
            items={[
                {
                    id: "general",
                    title: "Asset details",
                    isIndex: true,
                    component: () => <AssetDetailsTab assetData={target} withAssetLink />
                },
                {
                    id: "scan",
                    title: "Scan details",
                    path: ASSET_SCAN_DETAILS_PATHS.SCAN_DETAILS,
                    component: () => <ScanDetailsTab scanData={scan} />
                },
                {
                    id: "findings",
                    title: "Findings",
                    path: ASSET_SCAN_DETAILS_PATHS.FINDINGHS,
                    component: () => <Findings findingsSummary={scan?.summary} />
                }
            ]}
            withInnerPadding={false}
        />
    )
}

const AssetScanDetails = () => (
    <DetailsPageWrapper
        backTitle="Asset scans"
        url={APIS.ASSET_SCANS}
        getTitleData={({scan, target}) => {
            const {startTime, scanConfigSnapshot} = scan || {};

            return ({
                title: target?.targetInfo?.instanceID,
                subTitle: `scanned by ${scanConfigSnapshot?.name} on ${formatDate(startTime)}`
            })
        }}
        detailsContent={props => <DetailsContent {...props} />}
        withPadding
    />
)

export default AssetScanDetails;