-- How far people watched a video ad, as the ad network counts it, beside
-- the plays already kept in video_views (Meta: 3 seconds or more; TikTok:
-- 2 seconds or more). video_thruplays are plays to the end or for long
-- enough to count as watched (Meta's ThruPlay: 15 seconds or the whole
-- video; TikTok: 6 seconds). The quartiles are plays that reached a
-- quarter, half, three quarters and all of the video. Null where the
-- network didn't say, as for every other delivery number.
ALTER TABLE ad_metrics_daily
    ADD COLUMN video_thruplays bigint CHECK (video_thruplays >= 0),
    ADD COLUMN video_p25       bigint CHECK (video_p25 >= 0),
    ADD COLUMN video_p50       bigint CHECK (video_p50 >= 0),
    ADD COLUMN video_p75       bigint CHECK (video_p75 >= 0),
    ADD COLUMN video_p100      bigint CHECK (video_p100 >= 0);
